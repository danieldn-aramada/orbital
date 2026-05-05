package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"

	"github.com/labstack/echo/v4"
)

type DataCenter struct {
	dev       bool
	dgraphURL string
	fragment  *template.Template
}

func NewDataCenter(dgraphURL string, dev bool) *DataCenter {
	return &DataCenter{
		dgraphURL: dgraphURL,
		dev:       dev,
		fragment:  parseDataCenterFragment(),
	}
}

func parseDataCenterFragment() *template.Template {
	return template.Must(template.ParseFiles(
		"web/templates/fragments/datacenter-tab.gohtml",
	))
}

type dataCenterTabData struct {
	ID        string
	Name      string
	CreatedBy string
	CreatedAt string
}

func (h *DataCenter) Tab(c echo.Context) error {
	id := c.Param("id")

	query := fmt.Sprintf(`{ getDataCenter(id: "%s") { id name createdBy createdAt } }`, id)
	body, _ := json.Marshal(map[string]string{"query": query})

	resp, err := http.Post(h.dgraphURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("dgraph query: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Data struct {
			GetDataCenter dataCenterTabData `json:"getDataCenter"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	tmpl := h.fragment
	if h.dev {
		tmpl = parseDataCenterFragment()
	}

	c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
	return tmpl.Execute(c.Response().Writer, result.Data.GetDataCenter)
}
