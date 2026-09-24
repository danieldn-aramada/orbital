package handler

import (
	"bytes"
	"html/template"
	"regexp"
	"strings"
	"testing"

	"github.com/armada/orbital/internal/web/data/component"
)

// TestEditModalPrefixIsConsistent guards the defect that consolidating the four
// per-type edit modals exposed: the network-device modal used `networkdevice`
// for the modal element id and `network-device` for every inner id
// (json-editor, submit button, error span, data islands, close attribute).
//
// Nothing enforced the pairing. orbital.js's opener keys on the modal id while
// initConfigItemEditor looks up the inner ids, so the two could — and did —
// drift apart silently: the modal opens and the editor never initialises, or
// vice versa. One template makes a split impossible to introduce accidentally,
// and this makes it impossible to introduce deliberately either.
func TestEditModalPrefixIsConsistent(t *testing.T) {
	t.Chdir("../..")

	tmpl, err := template.ParseFiles("web/templates/shared/components/edit-modal.gohtml")
	if err != nil {
		t.Fatalf("parse edit-modal.gohtml: %v", err)
	}

	// Every prefix the app ships. A new parent family adds one here.
	for _, prefix := range []string{"srv", "dc", "cluster", "network-device"} {
		t.Run(prefix, func(t *testing.T) {
			var buf bytes.Buffer
			err := tmpl.ExecuteTemplate(&buf, "edit-modal.gohtml", component.EditModal{
				Prefix: prefix, Title: "Edit Thing",
				DomID: "DOM1", OrbID: "ns:thing-1", Version: 3,
				CurrentUser: "someone@example.com",
			})
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			html := buf.String()

			// Each of these is read by a different piece of JS. If any one uses
			// a different prefix, that piece silently stops finding its element.
			for _, want := range []string{
				`id="edit-modal-` + prefix + `-DOM1"`,   // orbital.js opener
				`id="` + prefix + `-json-editor-DOM1"`,  // JSONEditor mount
				`id="` + prefix + `-edit-submit-DOM1"`,  // submit binding
				`id="` + prefix + `-edit-error-DOM1"`,   // showError target
				`id="` + prefix + `-edit-data-DOM1"`,    // data island
				`id="` + prefix + `-edit-targets-DOM1"`, // targets island
			} {
				if !strings.Contains(html, want) {
					t.Errorf("missing %s", want)
				}
			}

			// The close button is deliberately prefix-FREE: it sits inside the
			// modal, so one delegated handler finds it via closest('.modal').
			// A per-type attribute name is also impossible here — html/template
			// will not interpolate into an attribute name, so
			// data-{{.Prefix}}-modal-close silently renders nothing.
			if !strings.Contains(html, "data-modal-close") {
				t.Error("missing data-modal-close")
			}
			if strings.Contains(html, "-modal-close=") {
				t.Error("a per-type modal-close attribute reappeared; the close handler is prefix-free")
			}

			// And nothing may use a DIFFERENT prefix: catches a half-done rename.
			idRe := regexp.MustCompile(`id="(?:edit-modal-)?([a-z-]+?)-(?:json-editor|edit-submit|edit-error|edit-data|edit-targets)-DOM1"`)
			for _, m := range idRe.FindAllStringSubmatch(html, -1) {
				if m[1] != prefix {
					t.Errorf("element id uses prefix %q, want %q — the modal and its inner elements must agree", m[1], prefix)
				}
			}
		})
	}
}

// TestEditModalOmitsUnusedReloadAttrs pins that data-reload-url/-target appear
// ONLY when a reload URL is set.
//
// Only the server opener reads them (orbital.js); cluster and network-device
// carried them for months while using a reloadFn instead, and the data-center
// modal never had them at all. Emitting them unconditionally would restore that
// dead markup and re-suggest a wiring that does not exist.
func TestEditModalOmitsUnusedReloadAttrs(t *testing.T) {
	t.Chdir("../..")
	tmpl := template.Must(template.ParseFiles("web/templates/shared/components/edit-modal.gohtml"))

	render := func(m component.EditModal) string {
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, "edit-modal.gohtml", m); err != nil {
			t.Fatalf("execute: %v", err)
		}
		return buf.String()
	}

	without := render(component.EditModal{Prefix: "dc", DomID: "D"})
	if strings.Contains(without, "data-reload-url") || strings.Contains(without, "data-reload-target") {
		t.Error("reload attributes rendered with no ReloadURL set — dead markup")
	}

	with := render(component.EditModal{Prefix: "srv", DomID: "D", ReloadURL: "/servers/x", ReloadTarget: "t"})
	if !strings.Contains(with, `data-reload-url="/servers/x"`) || !strings.Contains(with, `data-reload-target="t"`) {
		t.Error("reload attributes missing when ReloadURL is set")
	}
	// Bare path, never BasePath-prefixed — orbital.js does BASE + dataset.reloadUrl.
	if strings.Contains(with, `data-reload-url="/orbital`) {
		t.Error("reload URL must be a bare path; JS prepends BASE")
	}
}
