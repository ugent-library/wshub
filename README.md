[![Go Reference](https://pkg.go.dev/badge/github.com/ugent-library/catbird.svg)](https://pkg.go.dev/github.com/ugent-library/catbird)

# ugent-library/catbird

A simple and robust Go websocket connection hub 

```go
hub, _ := catbird.New(catbird.Config{Secret: secret})
defer hub.Stop()

mux := http.NewServeMux()
mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
    hub.HandleWebsocket(w, r, r.URL.Query().Get("token"))
})

ticker := time.NewTicker(time.Second)
defer ticker.Stop()

go func() {
    for {
        select {
        case t := <-ticker.C:
            hub.Send("clock", fmt.Println("Current time:", t))
        }
    }
}()

http.ListenAndServe("localhost:3000", mux);
```

## Install

```sh
go get -u github.com/ugent-library/catbird
```