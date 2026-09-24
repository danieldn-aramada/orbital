package component

import "html/template"

// EditModal is the render context for the single shared edit-modal template
// (web/templates/shared/components/edit-modal.gohtml).
//
// Until 2026-09-23 there were four near-identical templates — one per parent
// ConfigItem family — differing only in an id prefix, a title, a reload target
// and one or two extra data attributes. That copy was the tax UI.md warns about
// on every new parent family ("a 5-file copy"), and it had already produced two
// defects: the network-device modal used `networkdevice` for the modal id and
// `network-device` for every inner id, and the data-center modal was alone in
// carrying no reload target at all.
//
// (This type replaced an unrelated, unreferenced EditModal that belonged to the
// hand-written create/delete forms deleted in the same pass.)
type EditModal struct {
	// Prefix namespaces every id and data attribute this modal owns:
	// "srv", "dc", "cluster", "network-device". It MUST match the prefix the
	// opener in orbital.js keys on (`edit-modal-<prefix>-<domID>`) and the one
	// initConfigItemEditor looks for on the inner elements — they were allowed
	// to disagree once, and nothing caught it.
	Prefix string

	// Title is the modal-card heading, e.g. "Edit Server".
	Title string

	DomID       string
	OrbID       string
	Version     int
	CurrentUser string

	// ReloadURL is a BARE path (no BasePath — JS prepends it, per UI.md's URL
	// construction rule). Empty means the page provides no post-save reload.
	ReloadURL    string
	ReloadTarget string

	// Typename is set for the polymorphic families (cluster, network device)
	// where the concrete type cannot be inferred from the prefix.
	Typename string

	// Idrac* are the Server family's owned child, carried so the editor can
	// version-guard it alongside the parent.
	IdracOrbID   string
	IdracVersion int

	EditDataJSON    template.JS
	EditTargetsJSON template.JS
}
