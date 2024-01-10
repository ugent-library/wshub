package main

import (
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/ugent-library/hub"
	"github.com/ugent-library/hub/natsbridge"
)

var roomTmpl = template.Must(template.ParseFiles(
	"./layout.html.tmpl",
	"./room.html.tmpl",
))

func main() {
	var port int

	flag.IntVar(&port, "port", 3000, "server port")
	flag.Parse()

	bridge, err := natsbridge.New("")
	if err != nil {
		log.Fatal(err)
	}
	chatHub := hub.New(hub.Config{
		Bridge: bridge,
	})

	r := chi.NewRouter()
	r.Use(middleware.Logger)

	r.Post("/room/{room}", func(w http.ResponseWriter, r *http.Request) {
		chatHub.Send(chi.URLParam(r, "room"), []byte(`<ul id="messages" hx-swap-oob="beforeend"><li>`+
			r.FormValue("user")+`: `+r.FormValue("msg")+
			`</li></ul>`))
	})

	r.Get("/room/{room}/user/{user}/ws", func(w http.ResponseWriter, r *http.Request) {
		chatHub.HandleWebsocket(w, r, chi.URLParam(r, "user"), []string{chi.URLParam(r, "room")})
	})

	r.Get("/room/{room}/user/{user}/precense", func(w http.ResponseWriter, r *http.Request) {
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
