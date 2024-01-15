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

var homeTmpl = template.Must(template.ParseFiles(
	"./layout.html.tmpl",
	"./home.html.tmpl",
))

var chatTmpl = template.Must(template.ParseFiles(
	"./layout.html.tmpl",
	"./chat.html.tmpl",
))

type chatVars struct {
	Token string
	Room  string
	User  string
}

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
	hub := catbird.New(catbird.Config{
		Secret: []byte("MuxdvYHUQyxbQ2jpf4QqR6Aydh068CZC"),
		Bridge: bridge,
	})

	r := chi.NewRouter()
	r.Use(middleware.Logger)

	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		if err := homeTmpl.ExecuteTemplate(w, "layout", nil); err != nil {
			log.Print(err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
	})

	r.Post("/", func(w http.ResponseWriter, r *http.Request) {
		token, err := hub.Encrypt(r.FormValue("user"), []string{r.FormValue("room")})
		if err != nil {
			log.Print(err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		vars := chatVars{
			Token: token,
			Room:  r.FormValue("room"),
			User:  r.FormValue("user"),
		}
		if err := chatTmpl.ExecuteTemplate(w, "layout", vars); err != nil {
			log.Print(err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
	})

	r.Get("/ws", func(w http.ResponseWriter, r *http.Request) {
		if err := hub.HandleWebsocket(w, r, r.URL.Query().Get("token")); err != nil {
			log.Print(err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
	})

	r.Post("/chat", func(w http.ResponseWriter, r *http.Request) {
		user, topics, err := hub.Decrypt(r.FormValue("token"))
		if err != nil {
			log.Print(err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		hub.Send(topics[0], []byte(`<ul id="messages" hx-swap-oob="beforeend"><li>`+
			`<span class="badge rounded-pill text-bg-secondary">`+
			user+`</span> `+r.FormValue("msg")+
			`</li></ul>`))
	})

	r.Get("/presence", func(w http.ResponseWriter, r *http.Request) {
		_, topics, err := hub.Decrypt(r.URL.Query().Get("token"))
		if err != nil {
			log.Print(err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		users := hub.Presence(topics[0])
		sort.Strings(users)
		for i, user := range users {
			users[i] = `<span class="badge rounded-pill text-bg-info">` + user + `</span>`
		}
		w.Write([]byte(`<div id="users">` + strings.Join(users, " ") + `<div>`))
	})

	// r.Get("/broadcast", func(w http.ResponseWriter, r *http.Request) {
	// 	if err := broadcastTmpl.ExecuteTemplate(w, "layout", nil); err != nil {
	// 		log.Print(err)
	// 		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	// 	}
	// })

	// r.Post("/broadcast", func(w http.ResponseWriter, r *http.Request) {
	// 	hub.Send("*", []byte(`<ul id="messages" hx-swap-oob="beforeend"><li>`+
	// 		`<div class="alert alert-primary" role="alert">`+
	// 		r.FormValue("msg")+
	// 		`</div></ul>`))
	// })

	addr := fmt.Sprintf("localhost:%d", port)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatal(err)
	}
}
