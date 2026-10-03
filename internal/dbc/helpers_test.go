package dbc

import "strings"

func stringsReader(s string) *strings.Reader { return strings.NewReader(s) }

func asErrorList(err error, target *ErrorList) bool {
	el, ok := err.(ErrorList)
	if !ok {
		return false
	}
	*target = el
	return true
}
