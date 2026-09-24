package component

// LayersModal is the render context for the shared OCI layers modal
// (web/templates/shared/partials/layers-modal.gohtml).
//
// orbital and orb each had their own copy of that template. They rendered the
// same table from differently-named fields (.LayerRows/.HasLayers vs .Layers),
// and orb's carried a header comment promising its columns "mirror orbital's"
// — a mirror maintained by hand, with no test. It had already drifted: orbital
// grew an `orbital` producer pill that orb's copy never got.
//
// The one genuine difference is the Dispatch column, which only orb can fill
// (orbital does not dispatch layers to consumers). That is a flag, not a
// reason for two templates.
type LayersModal struct {
	Tag string

	// ShowDispatch renders the Dispatch column. orb sets it; orbital does not.
	ShowDispatch bool

	// EmptyMessage differs by app because the absence means different things:
	// orbital has no stored layer metadata for an artifact it published, orb
	// has no layer records for an import it performed.
	EmptyMessage string

	Rows []LayerRow
}

type LayerRow struct {
	Position    int
	Producer    string
	MediaType   string
	SizeDisplay string
	Digest      string

	// IsOrbitalNative is orbital's fallback attribution for artifacts published
	// before the producer annotation existed. orb leaves it false.
	IsOrbitalNative bool

	// Role and Dispatch are orb's: "graph" layers are never dispatched, so they
	// render an em dash rather than an empty cell.
	Role     string
	Dispatch *LayerDispatch
}

type LayerDispatch struct {
	ConsumerName string
	URL          string
	StatusCode   int
	Error        string
}
