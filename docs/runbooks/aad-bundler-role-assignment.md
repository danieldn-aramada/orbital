# Runbook — Assign orbital-viewer App Role to the Orbital-Netbox service principal

**Audience:** Azure AD administrator with at least **Application Administrator**, **Cloud Application Administrator**, **Privileged Role Administrator**, or **Global Administrator** rights on the Armada tenant.

**Time required:** ~3 minutes.

**Why this is needed:** The Orbital-Netbox application registration uses OAuth2 client credentials grant to authenticate internal service-to-service calls to itself (see [ADR 010](../decisions/010-bundler-service-auth.md) for design rationale). For Microsoft Entra to include the `roles` claim in the issued JWT, the calling service principal needs an **App Role Assignment** on the resource service principal. Because the caller and resource are the same application, this self-assignment is not auto-created by the "Grant admin consent" button on App Registrations and must be added explicitly.

This runbook covers the UI-based assignment via the Enterprise Applications blade.

## Pre-conditions

Before running this runbook, the following must already be true (someone with appropriate access has already configured the Orbital-Netbox app registration):

- [ ] **Application ID URI** is set to `api://5fc832f6-843e-4207-93dd-b3c3a77c06f2`
  - Check: Microsoft Entra ID → App registrations → Orbital-Netbox → **Expose an API** → "Application ID URI" field at top
- [ ] **App Role** named `orbital-viewer` exists with **Allowed member types = Applications**
  - Check: same app reg → **App roles** → one row with Display name "Orbital API reader" (or similar), Value `orbital-viewer`, Member types `Applications`
- [ ] **API permission** added: orbital app → Application permissions → `orbital-viewer`
  - Check: same app reg → **API permissions** → table includes row "Orbital-Netbox / orbital-viewer / Application"
- [ ] **Admin consent** has been granted on the API permissions page
  - Check: same row shows Status: ✓ "Granted for {tenant name}"

If any of the above is missing, do those first before this runbook. They're covered in the bundler-service-auth implementation plan.

## What you'll see that signals this runbook is needed

After admin consent has been granted on App Registrations → API permissions, navigating to **Enterprise applications → Orbital-Netbox → Permissions** shows:

> *"No admin consented permissions found for the application"*

despite the App Registration UI showing consent granted. This is the symptom — App Role self-assignment was not created and the issued JWT lacks the `roles` claim.

## UI steps

1. Sign in to [Azure Portal](https://portal.azure.com) with an account that has at least **Application Administrator** role on the Armada tenant.
2. Navigate to **Microsoft Entra ID** → **Enterprise applications** (NOT "App registrations" — different blade).
3. In the application search box, type **Orbital-Netbox**. Click the matching row.
4. In the left sidebar of the Orbital-Netbox enterprise app, click **Users and groups**.
5. Click **+ Add user/group** at the top.
6. The "Add Assignment" panel opens with two sections:
   - **Users and groups** — currently shows "None Selected"
   - **Select a role** — currently shows "None Selected"
7. Click **Users and groups** → "None Selected":
   - In the search box on the right panel, type **Orbital-Netbox**
   - The application's service principal should appear in results. It may have a slightly different icon than human users.
   - Click the **Orbital-Netbox** row to select it (✓ appears next to it)
   - Click **Select** at the bottom of the panel
8. Click **Select a role** → "None Selected":
   - One role should be listed: **Orbital API reader** (Value: `orbital-viewer`, Type: `Application`)
   - Click it (✓ appears)
   - Click **Select**
9. Back on the Add Assignment panel, both sections now show their selections. Click **Assign** at the bottom.
10. You should be returned to the Users and Groups list, now showing one row:
    | Display Name | Object Type | Role Assigned |
    |---|---|---|
    | Orbital-Netbox | Application | Orbital API reader |

## Fallback if the service principal doesn't appear in step 7

Some tenants enforce a security control that hides service principals from the Add Assignment picker. If "Orbital-Netbox" doesn't appear when searching:

1. Open [Microsoft Graph Explorer](https://developer.microsoft.com/en-us/graph/graph-explorer) in a new tab
2. Sign in (top-right) with the same admin account
3. **First call:** GET the orbital service principal object ID
   - Method: **GET**
   - URL: `https://graph.microsoft.com/v1.0/servicePrincipals?$filter=appId eq '5fc832f6-843e-4207-93dd-b3c3a77c06f2'`
   - Click **Run query**
   - In the response, copy the `id` value (this is the SP object ID, not the application ID)
4. **Second call:** GET the orbital app's App Roles
   - Method: **GET**
   - URL: `https://graph.microsoft.com/v1.0/servicePrincipals/<paste-sp-id>` (replace placeholder)
   - In the response, find the `appRoles` array → entry with `"value": "orbital-viewer"` → copy its `id` (GUID)
5. **Third call:** create the role assignment
   - Method: **POST**
   - URL: `https://graph.microsoft.com/v1.0/servicePrincipals/<paste-sp-id>/appRoleAssignments`
   - Headers tab → add `Content-Type: application/json`
   - Request body tab → paste:
     ```json
     {
       "principalId": "<paste-sp-id>",
       "resourceId": "<paste-sp-id>",
       "appRoleId": "<paste-app-role-id>"
     }
     ```
   - Click **Run query**
   - Success = **201 Created** with the assignment details

## CLI alternative (if you prefer terminal)

```bash
TENANT=8f231c2a-9551-4b40-be17-5b24afe5e890
CLIENT_ID=5fc832f6-843e-4207-93dd-b3c3a77c06f2

az login --tenant $TENANT

SP_ID=$(az ad sp show --id $CLIENT_ID --query id -o tsv)
APP_ROLE_ID=$(az ad sp show --id $CLIENT_ID --query "appRoles[?value=='orbital-viewer'].id" -o tsv)

az rest --method POST \
  --uri "https://graph.microsoft.com/v1.0/servicePrincipals/$SP_ID/appRoleAssignments" \
  --headers "Content-Type=application/json" \
  --body "{
    \"principalId\": \"$SP_ID\",
    \"resourceId\": \"$SP_ID\",
    \"appRoleId\": \"$APP_ROLE_ID\"
  }"
```

Expected output: a JSON object with `"principalDisplayName": "Orbital-Netbox"`.

A 403 here means the signed-in account lacks the required directory role. Use the UI path with an account that has it.

## Verification

After the assignment is created (wait ~30 seconds for AAD propagation), anyone can verify by minting a fresh client-credentials token:

```bash
TENANT=8f231c2a-9551-4b40-be17-5b24afe5e890
CLIENT_ID=5fc832f6-843e-4207-93dd-b3c3a77c06f2
CLIENT_SECRET=<read from K8s Secret 'orbital-secrets', key 'ORBITAL_OIDC_CLIENT_SECRET'>

curl -s -X POST "https://login.microsoftonline.com/$TENANT/oauth2/v2.0/token" \
  -d "grant_type=client_credentials" \
  -d "client_id=$CLIENT_ID" \
  -d "client_secret=$CLIENT_SECRET" \
  -d "scope=api://$CLIENT_ID/.default" \
  | python3 -m json.tool
```

Decode the middle part of the returned `access_token` (base64-url decode the JWT payload). The decoded JSON should now include:

```json
{
  "aud": "5fc832f6-...",
  "azp": "5fc832f6-...",
  "roles": ["orbital-viewer"],        ← THIS line appears after the assignment
  ...
}
```

You can also confirm in the Portal: **Enterprise applications → Orbital-Netbox → Permissions** now lists the assignment (the "No admin consented permissions" message is gone).

## What this enables

After the assignment:
- Tokens minted for the orbital app via client credentials include `roles: ["orbital-viewer"]`
- Orbital's bearer verifier (`internal/auth/bearer.go`) can optionally gate on this claim in the future (today it gates on `appid`/`azp` allowlist; the claim is forward-compat)
- cb-bundler — the internal service that needs this access path — can present its self-minted JWT to orbital's `/graphql` and be authenticated as `app:5fc832f6-...` in audit logs

## What does NOT change

- The user-login OIDC flow (humans signing into orbital via SSO) is unaffected.
- No new client secret is created — the existing `ORBITAL_OIDC_CLIENT_SECRET` is reused.
- Existing API consumers using user-context bearer tokens continue to work as before.

## Idempotency

Running the assignment twice returns a 409 Conflict on the second attempt, which is safe — the existing assignment remains in place.

## Removing the assignment

If needed for revocation:

- UI: **Enterprise applications → Orbital-Netbox → Users and groups** → select the row → **Remove** → Yes
- CLI: `az rest --method DELETE --uri "https://graph.microsoft.com/v1.0/servicePrincipals/$SP_ID/appRoleAssignments/<assignment-id>"`
