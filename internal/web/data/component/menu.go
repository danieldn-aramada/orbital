package component

type Menu struct {
	Sections []MenuSection `json:"sections"`
}

type MenuSection struct {
	Title string     `json:"title"`
	Items []MenuItem `json:"items"`
	Id    string     `json:"id"`
	Color string     `json:"color"`
	Icon  string     `json:"icon"`
}

type MenuItem struct {
	Title    string     `json:"title"`
	Link     string     `json:"link"`
	SubItems []MenuItem `json:"subItems"`
}
