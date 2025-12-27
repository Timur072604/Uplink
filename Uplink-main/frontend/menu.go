package main

import (
	"fmt"
	"syscall/js"
)

func renderMenu(a *App, tab string) {
	act := "bg-[#00f3ff] text-black shadow-[0_0_15px_#00f3ff]"
	inact := "hover:bg-[#00f3ff]/10 border border-transparent hover:border-[#00f3ff]/30"
	ds, ls, hs := inact, inact, inact
	cont := ""

	if tab == "dashboard" {
		ds = act
		displayRating := "0"
		if a.User != nil {
			displayRating = fmt.Sprintf("%d", a.User.Rating)
		}
		
		cont = `
			<div class="grid grid-cols-1 md:grid-cols-2 gap-6 relative z-10">
				<div class="hud-border p-6 bg-black/40 backdrop-blur-md border-[#00f3ff]/20">
					<div class="text-[10px] opacity-40 mb-1 font-mono tracking-widest text-[#00f3ff]">RANK</div>
					<div class="text-5xl font-black tracking-tighter text-white">#` + displayRating + `</div>
				</div>

				<div class="hud-border p-6 bg-black/40 backdrop-blur-md border-[#00f3ff]/20 text-right">
					<div class="text-[10px] opacity-40 mb-1 font-mono tracking-widest text-[#00f3ff]">SPEED</div>
					<div class="text-5xl font-black tracking-tighter text-[#00f3ff]">0.0<span class="text-sm opacity-30 ml-2">WPM</span></div>
				</div>

				<div onclick="createLobby()" class="md:col-span-2 hud-border p-6 bg-[#00f3ff]/5 border-[#00f3ff]/30 cursor-pointer group hover:bg-[#00f3ff]/15 transition-all text-center mb-6">
					<div class="text-[10px] opacity-40 mb-1 font-mono tracking-widest text-[#00f3ff]">GENERATE_SIGNAL</div>
					<div class="text-2xl font-bold tracking-[0.3em] text-[#00f3ff] group-hover:glow-text transition-all uppercase">
						CREATE_PRIVATE_LOBBY
					</div>
				</div>
			</div>

			<div class="relative z-10 mt-8">
				<div class="flex justify-between items-end mb-4 border-b border-[#00f3ff]/10 pb-2">
					<h3 class="text-sm tracking-[0.2em] opacity-70">DETECTED_SIGNALS</h3>
					<button onclick="refreshLobbies()" class="text-[10px] border border-[#00f3ff]/30 px-2 py-1 hover:bg-[#00f3ff]/10 transition-all">REFRESH_SCAN</button>
				</div>
				
				<div id="lobby-list" class="grid gap-4">
					<div class="text-center opacity-30 text-xs py-10 animate-pulse">SCANNING_FREQUENCIES...</div>
				</div>
			</div>`
		js.Global().Set("joinLobby", js.FuncOf(func(this js.Value, args []js.Value) any {
			if len(args) > 0 {
				id := args[0].String()
				a.navigate("/lobby/" + id)
			}
			return nil
		}))
		
		js.Global().Set("refreshLobbies", js.FuncOf(func(this js.Value, args []js.Value) any {
			a.fetchLobbies()
			return nil
		}))
		go a.fetchLobbies()

	} else if tab == "leaderboard" {
		ls = act
		cont = `<div class="opacity-40 tracking-[0.5em] text-center mt-20 text-xs animate-pulse font-mono z-10 relative">ESTABLISHING_UPLINK...</div>`
		a.fetchLeaderboard()
	} else if tab == "history" {
		hs = act
		cont = `<div class="opacity-40 tracking-[0.5em] text-center mt-20 text-xs animate-pulse font-mono z-10 relative uppercase">DECRYPTING_LOGS...</div>`
		a.fetchHistory()
	}

	html := `
    <div class="fixed inset-0 flex bg-transparent text-[#00f3ff] font-mono uppercase">
        
        <div class="w-72 border-r border-[#00f3ff]/20 bg-black/80 backdrop-blur-xl p-8 flex flex-col z-20">
            <div class="mb-20">
                <div class="text-3xl font-bold glow-text tracking-tighter" style="font-family: 'Orbitron';">UP<span class="opacity-20">LINK</span></div>
            </div>
            <nav class="flex-1 space-y-4">
                <button onclick="changeTab('dashboard')" class="w-full p-4 text-[10px] text-left tracking-[0.3em] font-bold transition-all ` + ds + `">DASHBOARD</button>
                <button onclick="changeTab('leaderboard')" class="w-full p-4 text-[10px] text-left tracking-[0.3em] font-bold transition-all ` + ls + `">LEADERBOARD</button>
                <button onclick="changeTab('history')" class="w-full p-4 text-[10px] text-left tracking-[0.3em] font-bold transition-all ` + hs + `">LOGS</button>
            </nav>
            <button onclick="logout()" class="border border-red-500/30 text-red-500/50 p-2 text-[9px] tracking-[0.5em] hover:bg-red-500/10">DISCONNECT</button>
        </div>

        <div class="flex-1 p-12 overflow-y-auto relative z-10 bg-transparent">
            <header class="flex justify-between items-end mb-12 border-b border-[#00f3ff]/10 pb-4">
                <div>
                    <div class="text-[9px] opacity-30 tracking-[0.4em]">PATH</div>
                    <div class="text-xl font-bold tracking-[0.2em]">ROOT/` + tab + `</div>
                </div>
                <div class="text-[9px] text-[#00f3ff] border border-[#00f3ff] px-3 py-1 mb-1 font-bold tracking-widest">LINK_OK</div>
            </header>

            <div id="menu-content">` + cont + `</div>
        </div>
    </div>`

    a.root.Set("innerHTML", html)
}