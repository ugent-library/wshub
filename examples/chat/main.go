package main

import (
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"sort"
	"strings"

	"github.com/ugent-library/catbird"
	"github.com/ugent-library/catbird/natsbridge"
)

var templates = template.Must(template.ParseGlob("./*.tmpl"))

func main() {
	var port int

	flag.IntVar(&port, "port", 3000, "server port")
	flag.Parse()

	bridge, err := natsbridge.New("")
	if err != nil {
		log.Fatal(err)
	}
	hub, err := catbird.New(catbird.Config{
		Bridge: bridge,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer hub.Stop()

	r := http.NewServeMux()

	r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if err := templates.ExecuteTemplate(w, "home.html.tmpl", nil); err != nil {
			log.Print(err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
	})

	r.HandleFunc("/join", func(w http.ResponseWriter, r *http.Request) {
		vars := struct {
			Room string
			User string
		}{
			Room: r.FormValue("room"),
			User: r.FormValue("user"),
		}
		if err := templates.ExecuteTemplate(w, "chat.html.tmpl", vars); err != nil {
			log.Print(err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
	})

	r.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		if err := hub.HandleWebsocket(w, r, r.URL.Query().Get("user"), []string{r.URL.Query().Get("room")}); err != nil {
			log.Print(err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
	})

	r.HandleFunc("/chat", func(w http.ResponseWriter, r *http.Request) {
		hub.Send(r.FormValue("room"), []byte(`<ul id="messages" hx-swap-oob="beforeend"><li>`+
			`<span class="badge rounded-pill text-bg-secondary">`+
			r.FormValue("user")+`</span> `+r.FormValue("msg")+
			`</li></ul>`))
	})

	r.HandleFunc("/presence", func(w http.ResponseWriter, r *http.Request) {
		users := hub.Presence(r.URL.Query().Get("room"))
		sort.Strings(users)
		for i, user := range users {
			users[i] = `<span class="badge rounded-pill text-bg-info">` + user + `</span>`
		}
		w.Write([]byte(`<div id="users">` + strings.Join(users, " ") + `<div>`))
	})

	addr := fmt.Sprintf("localhost:%d", port)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatal(err)
	}
}
