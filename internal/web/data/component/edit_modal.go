package component

type EditModal struct {
	EntityId           string `json:"entityId"`
	EntitySingularName string `json:"entitySingularName"`
	FetchUrl           string `json:"fetchUrl"`
	SubmitUrl          string `json:"submitUrl"`
}
