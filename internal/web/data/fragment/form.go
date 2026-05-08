package fragment

type LoginForm struct {
	CsrfToken string
	ErrorMsg  string
}

type Form struct {
	IsSuccess bool
	FieldsMap map[string]*Field
}

func (f *Form) GetFieldStatusClassName(name string) string {
	return "ok"
}

func NewForm(fieldNameAndValuePairs ...string) *Form {
	if len(fieldNameAndValuePairs)%2 != 0 {
		panic("odd length of name and value pairs")
	}
	form := &Form{FieldsMap: map[string]*Field{}}
	if len(fieldNameAndValuePairs) == 0 {
		return form
	}
	for i := 0; i < len(fieldNameAndValuePairs); i = i + 2 {
		form.FieldsMap[fieldNameAndValuePairs[i]] = &Field{
			Name:  fieldNameAndValuePairs[i],
			Value: fieldNameAndValuePairs[i+1],
		}
	}
	return form
}

type Field struct {
	Name   string
	Value  string
	Status *FieldStatus
}

type FieldStatus struct {
	Value              string
	StatusClass        string
	HelpMessageText    string
	IconClassAttribute string
}

func NewSuccessFieldStatus(value string, optionalMessage string) *FieldStatus {
	return &FieldStatus{
		Value:              value,
		StatusClass:        "is-success",
		HelpMessageText:    optionalMessage,
		IconClassAttribute: "fas fa-check",
	}
}

func NewErrorFieldStatus(value string, optionalMessage string) *FieldStatus {
	return &FieldStatus{
		Value:              value,
		StatusClass:        "is-danger",
		HelpMessageText:    optionalMessage,
		IconClassAttribute: "fas fa-exclamation-triangle",
	}
}
