#!/usr/bin/env bash
# End-to-end divergence reporting test.
# Assumes: make up + minikube running + orbital + orb + cb-bundler + cb-controller all up.
# Exercises: export → publish → orb import → SSA admin override → orb publish → orbital ingest → assert.
set -euo pipefail

ORBITAL=http://localhost:8001
ORB=http://localhost:8010
COOKIE_JAR=/tmp/orbital.cookies
DC_ORBID="colo:colo-galleon"
SERVER_ORBID="colo:5F206G4"
SERVER_OOB_IP="10.20.21.89"
SERVER_HOSTNAME="r11-u09.colo-galleon"
SERVER_TAG="5F206G4"

log() { printf '\033[1;36m==> %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31mFAIL: %s\033[0m\n' "$*"; exit 1; }

log "1/10 login (CSRF dance)"
rm -f "$COOKIE_JAR"
curl -s -c "$COOKIE_JAR" "$ORBITAL/" > /tmp/e2e-home.html
CSRF=$(grep -oE 'name="csrf" value="[^"]+"' /tmp/e2e-home.html | head -1 | sed 's/.*value="//;s/"$//')
[ -n "$CSRF" ] || fail "could not extract CSRF token"
LOGIN=$(curl -s -b "$COOKIE_JAR" -c "$COOKIE_JAR" -X POST "$ORBITAL/user/login" \
  --data-urlencode "email=admin@armada.ai" --data-urlencode "password=admin" \
  --data-urlencode "csrf=$CSRF" -w '%{http_code}' -o /tmp/e2e-login.out)
[ "$LOGIN" = "200" ] || fail "login HTTP $LOGIN"
grep -q "Invalid" /tmp/e2e-login.out && fail "login rejected — run 'bash scripts/seed.sh' if PG was reset"

log "2/10 trigger export for $DC_ORBID"
JOB=$(curl -s -b "$COOKIE_JAR" -X POST "$ORBITAL/api/v1/export" \
  -H "Content-Type: application/json" -d "{\"orbId\":\"$DC_ORBID\"}")
JOB_ID=$(echo "$JOB" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
[ -n "$JOB_ID" ] || fail "no job id in: $JOB"

log "3/10 wait for export job $JOB_ID"
for i in $(seq 1 30); do
  STATUS=$(curl -s -b "$COOKIE_JAR" "$ORBITAL/api/v1/export/jobs/$JOB_ID" \
    | python3 -c 'import json,sys; print(json.load(sys.stdin).get("status",""))')
  [ "$STATUS" = "completed" ] && break
  [ "$STATUS" = "failed" ] && fail "export failed"
  sleep 2
done
[ "$STATUS" = "completed" ] || fail "export timed out (status=$STATUS)"

log "4/10 publish to OCI"
PUB=$(curl -s -b "$COOKIE_JAR" -X POST "$ORBITAL/api/v1/export/jobs/$JOB_ID/publish" \
  -H "Content-Type: application/json" -d '{}')
ART_ID=$(echo "$PUB" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("artifactId",""))')
[ -n "$ART_ID" ] || fail "no artifactId in: $PUB"
for i in $(seq 1 30); do
  ART=$(curl -s -b "$COOKIE_JAR" "$ORBITAL/api/v1/oci/artifacts/$ART_ID")
  PHASE=$(echo "$ART" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("status",""))')
  [ "$PHASE" = "completed" ] && break
  [ "$PHASE" = "failed" ] && fail "publish failed: $ART"
  sleep 2
done
TAG=$(echo "$ART" | python3 -c 'import json,sys; print(json.load(sys.stdin)["tag"])')

log "5/10 reset CR (avoid stale-state SSA conflicts from prior runs)"
kubectl delete configbundle colo-galleon --ignore-not-found=true >/dev/null

log "6/10 trigger orb import of tag $TAG"
curl -s -X POST "$ORB/api/v1/import" -H "Content-Type: application/json" -d "{\"tag\":\"$TAG\"}" >/dev/null
for i in $(seq 1 60); do
  ST=$(curl -s "$ORB/api/v1/import/status" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("phase") or d.get("status",""))')
  [ "$ST" = "done" ] || [ "$ST" = "completed" ] || [ "$ST" = "idle" ] && break
  [ "$ST" = "failed" ] && fail "orb import failed"
  sleep 2
done

log "7/10 wait for cb-controller to apply manifest (CR gets new digest)"
DIGEST=$(echo "$ART" | python3 -c 'import json,sys; print(json.load(sys.stdin)["digest"])')
for i in $(seq 1 60); do
  CUR=$(kubectl get configbundle colo-galleon -o jsonpath='{.status.lastAppliedDigest}' 2>/dev/null || true)
  [ "$CUR" = "$DIGEST" ] && break
  sleep 2
done
[ "$CUR" = "$DIGEST" ] || fail "CR digest never matched (expected $DIGEST, got $CUR). If it's a stale CR, run: kubectl delete configbundle colo-galleon && re-run"

log "8/10 apply local:admin override (idrac.sshEnabled=true on $SERVER_ORBID)"
cat > /tmp/e2e-override.yaml <<EOF
apiVersion: armada.ai/v1
kind: ConfigBundle
metadata:
  name: colo-galleon
  namespace: default
spec:
  datacenter: colo-galleon
  orbId: $DC_ORBID
  servers:
  - orbId: $SERVER_ORBID
    hostname: $SERVER_HOSTNAME
    oobIP: $SERVER_OOB_IP
    serviceTag: $SERVER_TAG
    idrac:
      sshEnabled: true
EOF
kubectl apply --server-side --field-manager=local:admin --force-conflicts -f /tmp/e2e-override.yaml >/dev/null

log "9/10 wait for cb-controller reporter to POST divergence to orb (event-driven + 5s debounce default)"
# `grep -c` prints "0" AND exits non-zero on no match, which makes `|| echo 0`
# emit a second "0" — breaks integer comparison. Use a helper that always
# yields a single integer line.
count_reports() {
  grep -c "overrides.*1\|reported divergence" /tmp/cb-controller.log 2>/dev/null | head -1
}
BEFORE=$(count_reports); BEFORE=${BEFORE:-0}
for i in $(seq 1 60); do
  AFTER=$(count_reports); AFTER=${AFTER:-0}
  [ "$AFTER" -gt "$BEFORE" ] && break
  sleep 2
done
[ "$AFTER" -gt "$BEFORE" ] || fail "reporter never fired — cb-controller may be down, or DIVERGENCE_REPORTER_DEBOUNCE is too long"

log "10/10 trigger orb publish + wait for orbital ingest, then assert"
curl -s -X POST "$ORB/api/v1/divergence/publish" >/dev/null
# Orbital ingester polls S3 on its own schedule (typically every 10s). Poll
# the result for up to 30s rather than racing a single sleep.
COUNT=0
for i in $(seq 1 15); do
  RESULT=$(curl -s -b "$COOKIE_JAR" "$ORBITAL/api/v1/divergences")
  COUNT=$(echo "$RESULT" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))' 2>/dev/null || echo 0)
  [ "$COUNT" -ge 1 ] && break
  sleep 2
done
[ "$COUNT" -ge 1 ] || fail "no divergence rows visible in orbital — got: $RESULT"

echo "$RESULT" | python3 -m json.tool
printf '\n\033[1;32mE2E PASS\033[0m — %s divergence row(s) visible in orbital\n' "$COUNT"
