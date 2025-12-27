package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"syscall/js"
)

func renderAuth(a *App, isReg bool) {
	title, btn, link := "ТРЕБУЕТСЯ ИДЕНТИФИКАЦИЯ", "СОЕДИНЕНИЕ", "СОЗДАТЬ НОВЫЙ ID"
	if isReg {
		title, btn, link = "РЕГИСТРАЦИЯ АГЕНТА", "ПОДТВЕРДИТЬ", "ВЕРНУТЬСЯ К ВХОДУ"
	}

	html := `
	<div class="fixed inset-0 flex flex-col items-center justify-center p-4">
		<div class="mb-12 text-center">
			<h1 class="text-6xl font-bold glow-text tracking-[0.2em]" style="font-family: 'Orbitron';">UP<span class="opacity-30">LINK</span></h1>
			<div class="text-[10px] tracking-[0.5em] mt-2 opacity-50">НЕЙРОННАЯ_СВЯЗЬ_УСТАНОВЛЕНА</div>
		</div>
		<div class="hud-border bg-black/40 p-8 w-full max-w-[380px] backdrop-blur-md relative">
			<div class="absolute -top-[2px] -left-[2px] w-4 h-4 border-t-2 border-l-2 border-[#00f3ff]"></div>
			<div class="absolute -bottom-[2px] -right-[2px] w-4 h-4 border-b-2 border-r-2 border-[#00f3ff]"></div>
			
			<h2 class="text-center mb-10 tracking-[0.2em] font-bold opacity-80 text-sm">` + title + `</h2>
			
			<div id="err-log" class="hidden mb-6 p-2 border border-red-500/50 text-red-400 text-[10px] text-center bg-red-500/10 font-mono"></div>
			
			<div class="space-y-6">
				<input id="login" type="text" placeholder="ЛОГИН" class="w-full p-3 text-xs tracking-widest uppercase">
				<input id="pass" type="password" placeholder="КЛЮЧ_ДОСТУПА" class="w-full p-3 text-xs tracking-widest uppercase">
				
				<button onclick="pressSubmit(` + fmt.Sprint(isReg) + `)" class="w-full bg-[#00f3ff] text-black font-bold py-4 hover:bg-white transition-all tracking-[0.2em] text-sm">
					` + btn + `
				</button>
			</div>
			
			<button onclick="switchView(` + fmt.Sprint(!isReg) + `)" class="w-full mt-8 text-[9px] opacity-30 hover:opacity-100 transition-all tracking-[0.2em]">
				> ` + link + `
			</button>
		</div>
	</div>`
	a.root.Set("innerHTML", html)
}

func handleAuth(a *App, isReg bool) {
	u := a.doc.Call("getElementById", "login").Get("value").String()
	p := a.doc.Call("getElementById", "pass").Get("value").String()
	
	url := "/api/v1/auth/login"
	if isReg {
		url = "/api/v1/auth/register"
	}

	go func() {
		d, _ := json.Marshal(map[string]string{"username": u, "password": p})
		res, err := http.Post(url, "application/json", bytes.NewBuffer(d))
		if err != nil {
			showError(a, "ОШИБКА_СЕТИ")
			return
		}
		defer res.Body.Close()

		var b map[string]string
		json.NewDecoder(res.Body).Decode(&b)

		if res.StatusCode == 200 {
			token := b["token"]
			js.Global().Get("localStorage").Call("setItem", "token", token)
			a.fetchUser() 
			a.navigate("/menu")
		} else {
			showError(a, b["error"])
		}
	}() 
} 

func showError(a *App, msg string) {
	el := a.doc.Call("getElementById", "err-log")
	translatedMsg := msg
	if msg == "invalid credentials" { 
		translatedMsg = "НЕВЕРНЫЕ_ДАННЫЕ_ДОСТУПА" 
	}
	
	el.Set("innerText", ">> " + translatedMsg)
	el.Get("classList").Call("remove", "hidden")
}