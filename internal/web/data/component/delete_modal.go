package component

type DeleteModal struct {
	EntityId           string `json:"entityId"`
	EntitySingularName string `json:"entitySingularName"`
	FetchUrl           string `json:"fetchUrl"`
	SubmitUrl          string `json:"submitUrl"`
}
