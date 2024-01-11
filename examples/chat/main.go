package main

import (
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/ugent-library/catbird"
	"github.com/ugent-library/catbird/natsbridge"
)

var roomTmpl = template.Must(template.ParseFiles(
	"./layout.html.tmpl",
	"./room.html.tmpl",
))

var broadcastTmpl = template.Must(template.ParseFiles(
	"./layout.html.tmpl",
	"./broadcast.html.tmpl",
))

func main() {
	var port int

	flag.IntVar(&port, "port", 3000, "server port")
	flag.Parse()

	bridge, err := natsbridge.New("")
	if err != nil {
		log.Fatal(err)
	}
	chatHub := catbird.New(catbird.Config{
		Bridge: bridge,
	})

	r := chi.NewRouter()
	r.Use(middleware.Logger)

	r.Get("/broadcast", func(w http.ResponseWriter, r *http.Request) {
		if err := broadcastTmpl.ExecuteTemplate(w, "layout", nil); err != nil {
			log.Print(err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
	})

	r.Post("/broadcast", func(w http.ResponseWriter, r *http.Request) {
		chatHub.Send("*", []byte(`<ul id="messages" hx-swap-oob="beforeend"><li>`+
			`<div class="alert alert-primary" role="alert">`+
			r.FormValue("msg")+
			`</div></ul>`))
	})

	r.Post("/room/{room}", func(w http.ResponseWriter, r *http.Request) {
		chatHub.Send(chi.URLParam(r, "room"), []byte(`<ul id="messages" hx-swap-oob="beforeend"><li>`+
			`<span class="badge rounded-pill text-bg-secondary">`+
			r.FormValue("user")+`</span> `+r.FormValue("msg")+
			`</li></ul>`))
	})

	r.Get("/room/{room}/presence", func(w http.ResponseWriter, r *http.Request) {
		users := chatHub.Presence(chi.URLParam(r, "room"))
		sort.Strings(users)
		for i, user := range users {
			users[i] = `<span class="badge rounded-pill text-bg-info">` + user + `</span>`
		}
		w.Write([]byte(`<div id="users">` + strings.Join(users, " ") + `<div>`))
	})

	r.Get("/room/{room}/user/{user}/ws", func(w http.ResponseWriter, r *http.Request) {
		chatHub.HandleWebsocket(w, r, chi.URLParam(r, "user"), []string{chi.URLParam(r, "room")})
	})

	r.Get("/room/{room}/user/{user}", func(w http.ResponseWriter, r *http.Request) {
		vars := &struct {
			Room string
			User string
		}{
			Room: chi.URLParam(r, "room"),
			User: chi.URLParam(r, "user"),
		}
		if err := roomTmpl.ExecuteTemplate(w, "layout", vars); err != nil {
			log.Print(err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
	})

	addr := fmt.Sprintf("localhost:%d", port)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatal(err)
	}
}
