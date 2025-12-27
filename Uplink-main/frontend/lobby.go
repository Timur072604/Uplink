package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"syscall/js"
)

func (a *App) renderLobbyPage(roomID string) {
	fmt.Println("Entering lobby:", roomID)
	a.CurrentRoomID = roomID

	html := `
    <div class="fixed inset-0 flex flex-col bg-transparent text-[#00f3ff] font-mono uppercase overflow-hidden">
        <header class="p-6 border-b border-[#00f3ff]/20 bg-black/40 backdrop-blur-md flex justify-between items-center">
            <div>
                <div class="text-[10px] opacity-40 tracking-[0.5em]">INSTANCE_ID</div>
                <div class="text-2xl font-bold text-white shadow-[#00f3ff] drop-shadow-md">` + roomID + `</div>
            </div>
            <div class="flex gap-6">
                <button id="exit-btn" class="hud-border px-4 py-2 hover:bg-red-500/20 border-red-500/50 text-red-500 text-xs transition-all">
                    ABORT_SESSION
                </button>
            </div>
        </header>

        <main class="flex-1 flex p-6 gap-6 overflow-hidden">
            <div class="w-64 flex flex-col gap-4">
                <div id="host-settings" class="hud-border bg-black/60 p-4 hidden">
                    <h3 class="text-[10px] mb-4 tracking-[0.3em] border-b border-[#00f3ff]/20 pb-2">SESSION_CONFIG</h3>
                    <div class="flex flex-col gap-4">
                        <div class="flex flex-col gap-1">
                            <label class="text-[9px] opacity-60">MAX_AGENTS</label>
                            <select id="max-players-select" class="bg-black border border-[#00f3ff]/30 text-[#00f3ff] p-1 text-xs focus:outline-none focus:border-[#00f3ff]">
                                <option value="1">1 (SOLO)</option>
                                <option value="2">2 (DUEL)</option>
                                <option value="3">3 (TRIO)</option>
                                <option value="4">4 (SQUAD)</option>
                                <option value="8">8 (RAID)</option>
                            </select>
                        </div>
                        <div class="flex flex-col gap-1">
                            <label class="text-[9px] opacity-60">LANGUAGE</label>
                            <select id="language-select" class="bg-black border border-[#00f3ff]/30 text-[#00f3ff] p-1 text-xs focus:outline-none focus:border-[#00f3ff]">
                                <option value="ru">RUSSIAN (RU)</option>
                                <option value="en">ENGLISH (EN)</option>
                            </select>
                        </div>
                    </div>
                </div>

                <div class="hud-border bg-black/60 p-4 flex-1 overflow-y-auto">
                    <h3 class="text-[10px] mb-4 tracking-[0.3em] border-b border-[#00f3ff]/20 pb-2">CONNECTED_AGENTS</h3>
                    <div id="player-list" class="space-y-3 font-sans normal-case"></div>
                </div>
                
                <button id="start-btn" style="display: none;" class="bg-[#00f3ff] text-black py-4 font-bold hover:bg-white transition-all tracking-[.3em] text-sm shadow-[0_0_15px_rgba(0,243,255,0.5)]">
                    START_UPLINK
                </button>
            </div>

            <div class="flex-1 flex flex-col hud-border bg-black/40 backdrop-blur-sm overflow-hidden">
                <div class="p-2 border-b border-[#00f3ff]/10 text-[10px] opacity-50">SECURE_CHANNEL_v4.2</div>
                <div id="chat-messages" class="flex-1 p-4 overflow-y-auto space-y-2 text-sm normal-case font-sans"></div>
                <div class="p-4 border-t border-[#00f3ff]/20 bg-black/20 flex gap-2">
                    <input id="chat-input" type="text" placeholder="TYPE MESSAGE..." 
                        class="flex-1 bg-transparent border border-[#00f3ff]/30 p-2 text-[#00f3ff] focus:outline-none focus:border-[#00f3ff] placeholder:opacity-30">
                    <button id="chat-send" class="px-4 py-2 bg-[#00f3ff]/10 border border-[#00f3ff]/50 hover:bg-[#00f3ff]/30">SEND</button>
                </div>
            </div>
        </main>
    </div>`

	a.root.Set("innerHTML", html)

	var socket js.Value

	sendSettings := func() {
		if socket.IsUndefined() || socket.IsNull() { return }
		maxVal, _ := strconv.Atoi(a.doc.Call("getElementById", "max-players-select").Get("value").String())
		langVal := a.doc.Call("getElementById", "language-select").Get("value").String()

		fmt.Printf("[LOBBY] Sending Update: Max=%d, Lang=%s\n", maxVal, langVal)

		msg := map[string]any{
			"type": "update_settings",
			"payload": map[string]any{
				"max_players": maxVal,
				"language":    langVal,
			},
		}
		data, _ := json.Marshal(msg)
		socket.Call("send", string(data))
	}

	a.doc.Call("getElementById", "max-players-select").Set("onchange", js.FuncOf(func(this js.Value, args []js.Value) any {
		sendSettings()
		return nil
	}))
	a.doc.Call("getElementById", "language-select").Set("onchange", js.FuncOf(func(this js.Value, args []js.Value) any {
		sendSettings()
		return nil
	}))

	a.doc.Call("getElementById", "exit-btn").Set("onclick", js.FuncOf(func(this js.Value, args []js.Value) any {
		a.navigate("/menu")
		return nil
	}))

	sendChat := func() {
		input := a.doc.Call("getElementById", "chat-input")
		text := input.Get("value").String()
		if text != "" && !socket.IsUndefined() {
			msg := map[string]any{"type": "chat_message", "payload": map[string]any{"text": text}}
			jsonMsg, _ := json.Marshal(msg)
			socket.Call("send", string(jsonMsg))
			input.Set("value", "")
		}
	}
	a.doc.Call("getElementById", "chat-send").Set("onclick", js.FuncOf(func(this js.Value, args []js.Value) any {
		sendChat()
		return nil
	}))

	go func() { socket = a.setupLobbyWS(roomID) }()
}

func (a *App) setupLobbyWS(roomID string) js.Value {
	protocol := "ws://"
	if js.Global().Get("location").Get("protocol").String() == "https:" { protocol = "wss://" }

	ls := js.Global().Get("localStorage")
	tokenVal := ls.Call("getItem", "auth_token")
	if tokenVal.IsNull() { tokenVal = ls.Call("getItem", "token") }
	token := ""
	if !tokenVal.IsNull() && !tokenVal.IsUndefined() { token = tokenVal.String() }

	url := fmt.Sprintf("%s%s/ws?room_id=%s&token=%s", protocol, js.Global().Get("location").Get("host").String(), roomID, token)
	ws := js.Global().Get("WebSocket").New(url)

	ws.Set("onmessage", js.FuncOf(func(this js.Value, args []js.Value) any {
		jsonData := args[0].Get("data").String()
		var rawMsg struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal([]byte(jsonData), &rawMsg); err != nil { return nil }

		switch rawMsg.Type {
		case "player_joined", "lobby_update":
			a.updateAgentsUI(rawMsg.Payload)

		case "update_settings":
			var settings struct {
				MaxPlayers int    `json:"max_players"`
				Language   string `json:"language"`
			}
			if err := json.Unmarshal(rawMsg.Payload, &settings); err == nil {
				maxEl := a.doc.Call("getElementById", "max-players-select")
				langEl := a.doc.Call("getElementById", "language-select")
				
				if !maxEl.IsNull() && maxEl.Get("value").String() != strconv.Itoa(settings.MaxPlayers) {
					maxEl.Set("value", strconv.Itoa(settings.MaxPlayers))
				}
				if !langEl.IsNull() && langEl.Get("value").String() != settings.Language {
					langEl.Set("value", settings.Language)
				}
			}

		case "chat_message":
			a.appendChatMessage(rawMsg.Payload)
		}
		return nil
	}))

	return ws
}

func (a *App) updateAgentsUI(payload json.RawMessage) {
	var players []struct {
		UserID   string `json:"user_id"`
		Username string `json:"username"`
		IsOwner  bool   `json:"is_owner"`
	}
	if err := json.Unmarshal(payload, &players); err != nil { return }
	
	playerListEl := a.doc.Call("getElementById", "player-list")
	if playerListEl.IsNull() { return }

	isImOwner := false
	html := ""
	for _, p := range players {
		isMe := (a.User != nil && p.UserID == a.User.ID)
		if isMe && p.IsOwner { isImOwner = true }

		ownerBadge := ""
		if p.IsOwner { ownerBadge = ` <span class="text-[9px] border border-[#00f3ff] px-1 text-[#00f3ff]">HOST</span>` }
		
		html += fmt.Sprintf(`<div class="flex items-center gap-2 py-1">
			<div class="w-1.5 h-1.5 bg-[#00f3ff] shadow-[0_0_5px_#00f3ff]"></div>
			<div class="text-sm">%s%s</div>
		</div>`, p.Username, ownerBadge)
	}
	playerListEl.Set("innerHTML", html)
	if el := a.doc.Call("getElementById", "host-settings"); !el.IsNull() {
		el.Get("style").Set("display", "block")
	}
	
	if len(players) == 1 { isImOwner = true }

	for _, id := range []string{"max-players-select", "language-select", "start-btn"} {
		el := a.doc.Call("getElementById", id)
		if el.IsNull() { continue }
		if id == "start-btn" {
			if isImOwner { el.Get("style").Set("display", "block") } else { el.Get("style").Set("display", "none") }
		} else {
			el.Set("disabled", !isImOwner)
		}
	}
}

func (a *App) appendChatMessage(payload json.RawMessage) {
	var data struct {
		SenderName string `json:"sender_name"`
		Text       string `json:"text"`
	}
	if err := json.Unmarshal(payload, &data); err != nil { return }
	chatBox := a.doc.Call("getElementById", "chat-messages")
	if chatBox.IsNull() { return }

	msgHTML := fmt.Sprintf(`<div><strong>%s:</strong> %s</div>`, data.SenderName, data.Text)
	chatBox.Call("insertAdjacentHTML", "beforeend", msgHTML)
	chatBox.Set("scrollTop", chatBox.Get("scrollHeight"))
}