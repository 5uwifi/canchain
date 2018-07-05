//
// (at your option) any later version.
//
//

/*
Show nicely (but simple) formatted HTML error pages (or respond with JSON
if the appropriate `Accept` header is set)) for the http package.
*/
package http

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/5uwifi/canchain/basis/log4j"
	"github.com/5uwifi/canchain/basis/metrics"
	"github.com/5uwifi/canchain/basis/swarm/api"
	l "github.com/5uwifi/canchain/basis/swarm/log"
)

var templateMap map[int]*template.Template
var caseErrors []CaseError

var (
	htmlCounter = metrics.NewRegisteredCounter("api.http.errorpage.html.count", nil)
	jsonCounter = metrics.NewRegisteredCounter("api.http.errorpage.json.count", nil)
)

type ResponseParams struct {
	Msg       string
	Code      int
	Timestamp string
	template  *template.Template
	Details   template.HTML
}

type CaseError struct {
	Validator func(*Request) bool
	Msg       func(*Request) string
}

func init() {
	initErrHandling()
}

func initErrHandling() {
	//pages are saved as strings - get these strings
	genErrPage := GetGenericErrorPage()
	notFoundPage := GetNotFoundErrorPage()
	multipleChoicesPage := GetMultipleChoicesErrorPage()
	//map the codes to the available pages
	tnames := map[int]string{
		0: genErrPage, //default
		http.StatusBadRequest:          genErrPage,
		http.StatusNotFound:            notFoundPage,
		http.StatusMultipleChoices:     multipleChoicesPage,
		http.StatusInternalServerError: genErrPage,
	}
	templateMap = make(map[int]*template.Template)
	for code, tname := range tnames {
		//assign formatted HTML to the code
		templateMap[code] = template.Must(template.New(fmt.Sprintf("%d", code)).Parse(tname))
	}

	caseErrors = []CaseError{
		{
			Validator: func(r *Request) bool { return r.uri != nil && r.uri.Addr != "" && strings.HasPrefix(r.uri.Addr, "0x") },
			Msg: func(r *Request) string {
				uriCopy := r.uri
				uriCopy.Addr = strings.TrimPrefix(uriCopy.Addr, "0x")
				return fmt.Sprintf(`The requested hash seems to be prefixed with '0x'. You will be redirected to the correct URL within 5 seconds.<br/>
			Please click <a href='%[1]s'>here</a> if your browser does not redirect you.<script>setTimeout("location.href='%[1]s';",5000);</script>`, "/"+uriCopy.String())
			},
		}}
}

func ValidateCaseErrors(r *Request) string {
	for _, err := range caseErrors {
		if err.Validator(r) {
			return err.Msg(r)
		}
	}

	return ""
}

//"readme.md" and "readinglist.txt", a HTML page is returned with this two links.
func ShowMultipleChoices(w http.ResponseWriter, req *Request, list api.ManifestList) {
	msg := ""
	if list.Entries == nil {
		Respond(w, req, "Could not resolve", http.StatusInternalServerError)
		return
	}
	//make links relative
	//requestURI comes with the prefix of the ambiguous path, e.g. "read" for "readme.md" and "readinglist.txt"
	//to get clickable links, need to remove the ambiguous path, i.e. "read"
	idx := strings.LastIndex(req.RequestURI, "/")
	if idx == -1 {
		Respond(w, req, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	//remove ambiguous part
	base := req.RequestURI[:idx+1]
	for _, e := range list.Entries {
		//create clickable link for each entry
		msg += "<a href='" + base + e.Path + "'>" + e.Path + "</a><br/>"
	}
	Respond(w, req, msg, http.StatusMultipleChoices)
}

//(and return the correct HTTP status code)
func Respond(w http.ResponseWriter, req *Request, msg string, code int) {
	additionalMessage := ValidateCaseErrors(req)
	switch code {
	case http.StatusInternalServerError:
		log4j.Output(msg, log4j.LvlError, l.CallDepth, "ruid", req.ruid, "code", code)
	default:
		log4j.Output(msg, log4j.LvlDebug, l.CallDepth, "ruid", req.ruid, "code", code)
	}

	if code >= 400 {
		w.Header().Del("Cache-Control") //avoid sending cache headers for errors!
		w.Header().Del("ETag")
	}

	respond(w, &req.Request, &ResponseParams{
		Code:      code,
		Msg:       msg,
		Details:   template.HTML(additionalMessage),
		Timestamp: time.Now().Format(time.RFC1123),
		template:  getTemplate(code),
	})
}

func respond(w http.ResponseWriter, r *http.Request, params *ResponseParams) {
	w.WriteHeader(params.Code)
	if r.Header.Get("Accept") == "application/json" {
		respondJSON(w, params)
	} else {
		respondHTML(w, params)
	}
}

func respondHTML(w http.ResponseWriter, params *ResponseParams) {
	htmlCounter.Inc(1)
	err := params.template.Execute(w, params)
	if err != nil {
		log4j.Error(err.Error())
	}
}

func respondJSON(w http.ResponseWriter, params *ResponseParams) {
	jsonCounter.Inc(1)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(params)
}

func getTemplate(code int) *template.Template {
	if val, tmpl := templateMap[code]; tmpl {
		return val
	}
	return templateMap[0]
}
