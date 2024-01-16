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
	hub := catbird.New(catbird.Config{
		Secret: []byte("MuxdvYHUQyxbQ2jpf4QqR6Aydh068CZC"),
		Bridge: bridge,
	})
	defer hub.Stop()

	r := http.NewServeMux()

	r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if err := templates.ExecuteTemplate(w, "home.html.tmpl", nil); err != nil {
			log.Print(err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
	})

	r.HandleFunc("/join", func(w http.ResponseWriter, r *http.Request) {
		token, err := hub.Encrypt(r.FormValue("user"), []string{r.FormValue("room")})
		if err != nil {
			log.Print(err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		vars := struct {
			Token string
			Room  string
			User  string
		}{
			Token: token,
			Room:  r.FormValue("room"),
			User:  r.FormValue("user"),
		}
		if err := templates.ExecuteTemplate(w, "chat.html.tmpl", vars); err != nil {
			log.Print(err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
	})

	r.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		if err := hub.HandleWebsocket(w, r, r.URL.Query().Get("token")); err != nil {
			log.Print(err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
	})

	r.HandleFunc("/chat", func(w http.ResponseWriter, r *http.Request) {
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

	r.HandleFunc("/presence", func(w http.ResponseWriter, r *http.Request) {
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

	addr := fmt.Sprintf("localhost:%d", port)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatal(err)
	}
}
