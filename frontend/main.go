package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"syscall/js"
)

type UserData struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Rating   int    `json:"rating"`
}

type LobbyInfo struct {
	ID      string `json:"id"`
	Players int    `json:"players"`
	Status  string `json:"status"`
}

type App struct {
	doc  js.Value
	root js.Value
	User *UserData
	CurrentRoomID string
}



func main() {
	app := &App{
		doc: js.Global().Get("document"),
	}
	app.root = app.doc.Call("getElementById", "app")
	js.Global().Set("createLobby", js.FuncOf(func(this js.Value, args []js.Value) any {
        app.handleCreateLobby()
        return nil
    }))
	js.Global().Set("refreshLobbies", js.FuncOf(func(this js.Value, args []js.Value) any {
        app.fetchLobbies()
        return nil
    }))
	js.Global().Set("pressSubmit", js.FuncOf(func(this js.Value, args []js.Value) any {
        handleAuth(app, args[0].Bool())
        return nil
    }))

	js.Global().Set("pressSubmit", js.FuncOf(func(this js.Value, args []js.Value) any {
		handleAuth(app, args[0].Bool())
		return nil
	}))
	js.Global().Set("switchView", js.FuncOf(func(this js.Value, args []js.Value) any {
		renderAuth(app, args[0].Bool())
		return nil
	}))
	js.Global().Set("logout", js.FuncOf(func(this js.Value, args []js.Value) any {
		js.Global().Get("localStorage").Call("removeItem", "token")
		app.User = nil
		app.navigate("/")
		return nil
	}))
	js.Global().Set("changeTab", js.FuncOf(func(this js.Value, args []js.Value) any {
		renderMenu(app, args[0].String())
		return nil
	}))
	js.Global().Set("joinLobby", js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) > 0 {
			id := args[0].String()
			app.navigate("/lobby/" + id)
		}
		return nil
	}))

	app.router()
	select {} 
}


func (a *App) navigate(path string) {
	js.Global().Get("history").Call("pushState", nil, "", path)
	a.router()
}

func (a *App) router() {
    p := js.Global().Get("location").Get("pathname").String()
    t := js.Global().Get("localStorage").Call("getItem", "token")
    if t.IsNull() {
        if p != "/" {
            a.navigate("/")
        } else {
            renderAuth(a, false)
        }
        return
    }
    if a.User == nil {
        a.fetchUser()
        return 
    }
    if strings.HasPrefix(p, "/lobby/") {
        roomID := strings.TrimPrefix(p, "/lobby/")
        if roomID != "" {
            a.renderLobbyPage(roomID)
            return
        }
    }

    renderMenu(a, "dashboard")
}


func (a *App) fetchUser() {
    token := js.Global().Get("localStorage").Call("getItem", "token").String()
    if token == "" {
        return
    }
    go func() {
        client := &http.Client{}
        req, _ := http.NewRequest("GET", "/api/v1/users/me", nil)
        req.Header.Set("Authorization", "Bearer "+token)
        resp, err := client.Do(req)
        if err == nil && resp.StatusCode == 200 {
            json.NewDecoder(resp.Body).Decode(&a.User)
            a.router() 
        }
    }()
}

func (a *App) fetchLobbies() {
	go func() {
		token := js.Global().Get("localStorage").Call("getItem", "token").String()
		client := &http.Client{}
		req, _ := http.NewRequest("GET", "/api/v1/lobbies", nil)
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := client.Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()

		var lobbies []LobbyInfo
		if err := json.NewDecoder(resp.Body).Decode(&lobbies); err != nil {
			return
		}

		html := ""
		if len(lobbies) == 0 {
			html = `<div class="text-center opacity-20 text-xs py-10 border border-dashed border-[#00f3ff]/10">NO_ACTIVE_SIGNALS_FOUND</div>`
		} else {
			for _, l := range lobbies {
				html += fmt.Sprintf(`
                <div onclick="joinLobby('%s')" class="hud-border p-4 bg-black/40 hover:bg-[#00f3ff]/5 cursor-pointer flex justify-between items-center group transition-all border-[#00f3ff]/20">
                    <div>
                        <div class="text-[10px] opacity-40 font-mono">SIGNAL_SOURCE</div>
                        <div class="text-lg font-bold text-[#00f3ff] group-hover:glow-text transition-all">%s</div>
                    </div>
                    <div class="text-right">
                        <div class="text-[10px] opacity-40 font-mono">AGENTS</div>
                        <div class="text-xl font-bold">%d <span class="text-xs opacity-30">CONN</span></div>
                    </div>
                </div>`, l.ID, l.ID, l.Players)
			}
		}

		if el := a.doc.Call("getElementById", "lobby-list"); !el.IsNull() {
			el.Set("innerHTML", html)
		}
	}()
}

func (a *App) handleCreateLobby() {
	token := js.Global().Get("localStorage").Call("getItem", "token").String()
	go func() {
		client := &http.Client{}
		req, _ := http.NewRequest("POST", "/api/v1/lobby/create", nil)
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := client.Do(req)
		if err != nil || resp.StatusCode != 200 {
			fmt.Println("Ошибка создания лобби")
			return
		}
		defer resp.Body.Close()

		var res struct {
			RoomID string `json:"room_id"`
		}
		json.NewDecoder(resp.Body).Decode(&res)

		a.navigate("/lobby/" + res.RoomID)
	}()
}

func (a *App) fetchLeaderboard() {
	token := js.Global().Get("localStorage").Call("getItem", "token").String()
	go func() {
		client := &http.Client{}
		req, _ := http.NewRequest("GET", "/api/v1/leaderboard", nil)
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := client.Do(req)
		if err != nil || resp.StatusCode != 200 {
			return
		}
		defer resp.Body.Close()

		var res struct {
			Data []struct {
				Username string  `json:"username"`
				Rating   float64 `json:"rating"`
			} `json:"data"`
		}
		json.NewDecoder(resp.Body).Decode(&res)

		var rows string
		for i, l := range res.Data {
			isMe := a.User != nil && l.Username == a.User.Username
			cardClass := "bg-black/40 border-[#00f3ff]/10"
			nameClass := "text-white"
			if isMe {
				cardClass = "bg-[#00f3ff]/10 border-[#00f3ff]/40 shadow-[0_0_15px_rgba(0,243,255,0.1)]"
				nameClass = "text-[#00f3ff]"
			}

			rows += fmt.Sprintf(`
                <div class="hud-border %s p-6 mb-4 flex justify-between items-center transition-all hover:border-[#00f3ff]/30">
                    <div class="flex items-center gap-8">
                        <div class="text-3xl font-black font-mono opacity-20 w-16">#%02d</div>
                        <div>
                            <div class="text-[9px] text-[#00f3ff] opacity-40 tracking-[0.3em] mb-1">OPERATOR_ID</div>
                            <div class="text-2xl font-bold tracking-tight %s uppercase">%s</div>
                        </div>
                    </div>
                    <div class="text-right border-l border-[#00f3ff]/20 pl-10">
                        <div class="text-[9px] opacity-30 mb-1 tracking-[0.2em]">RATING_SCORE</div>
                        <div class="text-4xl font-black font-mono text-[#00f3ff]">%.0f</div>
                    </div>
                </div>`, cardClass, i+1, nameClass, l.Username, l.Rating)
		}

		if el := a.doc.Call("getElementById", "menu-content"); !el.IsNull() {
			if rows == "" {
				rows = `<div class="opacity-20 text-center mt-20 tracking-[1em] text-xs">NO_OPERATORS_ONLINE</div>`
			}
			el.Set("innerHTML", `<div class="max-w-5xl mx-auto py-4">` + rows + `</div>`)
		}
	}()
}

func (a *App) fetchHistory() {
	token := js.Global().Get("localStorage").Call("getItem", "token").String()
	go func() {
		client := &http.Client{}
		req, _ := http.NewRequest("GET", "/api/v1/users/history", nil)
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := client.Do(req)
		if err != nil || resp.StatusCode != 200 {
			return
		}
		defer resp.Body.Close()

		var res struct {
			Data []map[string]any `json:"data"`
		}
		json.NewDecoder(resp.Body).Decode(&res)

		var rows string
		for _, m := range res.Data {
			date, _ := m["date"].(string)
			if len(date) > 16 {
				date = date[:10] + " " + date[11:16]
			}

			rows += fmt.Sprintf(`
                <div class="hud-border bg-[#00f3ff]/5 p-8 mb-6 flex justify-between items-center group hover:bg-[#00f3ff]/10 transition-all">
                    <div class="flex-1 pr-10">
                        <div class="text-[10px] text-[#00f3ff]/40 font-mono mb-2 tracking-[0.2em]">%s</div>
                        <div class="text-2xl font-bold tracking-tight text-[#00f3ff] opacity-90 group-hover:opacity-100 uppercase">%v...</div>
                    </div>
                    <div class="flex items-center gap-12 border-l border-[#00f3ff]/10 pl-12">
                        <div class="text-center">
                            <div class="text-[10px] opacity-30 mb-1 tracking-widest">WPM</div>
                            <div class="text-4xl font-black font-mono">%.0f</div>
                        </div>
                        <div class="text-center">
                            <div class="text-[10px] opacity-30 mb-1 tracking-widest">ACC</div>
                            <div class="text-4xl font-black font-mono">%.1f<span class="text-sm opacity-30 ml-1">%%</span></div>
                        </div>
                        <div class="text-center min-w-[100px]">
                            <div class="text-[10px] opacity-30 mb-1 tracking-widest text-[#00f3ff]">RANK</div>
                            <div class="text-4xl font-black font-mono text-[#00f3ff] shadow-[#00f3ff]/20 drop-shadow-md">#%v</div>
                        </div>
                    </div>
                </div>`, date, m["text_preview"], m["wpm"], m["accuracy"], m["rank"])
		}

		if el := a.doc.Call("getElementById", "menu-content"); !el.IsNull() {
			if rows == "" {
				rows = `<div class="opacity-20 text-center mt-20 tracking-[0.5em] text-xs">NO_DATA_LOGS_FOUND</div>`
			}
			el.Set("innerHTML", `<div class="max-w-6xl mx-auto py-4">` + rows + `</div>`)
		}
	}()
}