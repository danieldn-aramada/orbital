package component

import "html/template"

type Table struct {
	Id           string        `json:"id"`
	VarName      template.JS   `json:"varName"`
	PluralName   string        `json:"pluralName"`
	SingularName string        `json:"singularName"`
	AjaxUrl      string        `json:"ajaxUrl"`
	Columns      []TableColumn `json:"columns"`
}

type TableColumn struct {
	Name      string `json:"name"`
	Visible   bool   `json:"visible"`
	ClassName string `json:"className"`
}
