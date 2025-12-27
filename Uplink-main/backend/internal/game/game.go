package game

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"uplink/backend/internal/db"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

const (
	AntiCheatMinMean     = 40.0
	AntiCheatMinVariance = 5.0
	MatchmakingBaseRange = 100.0
	MatchmakingTimeMult  = 50.0
	WPMCharCount         = 5.0
	EloKFactor           = 25.0

	InputBufferSize = 30
	MinInputSamples = 20
	StartDelay      = 3 * time.Second
	RoomIdleTimeout = 10 * time.Minute
	RoomUpdateTick  = 200 * time.Millisecond
	MatchmakerTick  = 2 * time.Second
	WriteWait       = 10 * time.Second

	StateLobby    = 0
	StateGame     = 1
	StateFinished = 2
	StateLoading  = 3
)

type Settings struct {
	Language   string `json:"language"`
	TextMode   string `json:"text_mode"`
	Category   string `json:"category"`
	TextID     int    `json:"text_id"`
	MaxPlayers int    `json:"max_players"`
}

type Client struct {
	ID, Username string
	Rating       int
	conn         *websocket.Conn
	room         *Room
	joinTime     time.Time
	send         chan any

	mu           sync.Mutex
	Progress     float64
	WPM          float64
	Finished     bool
	Disqualified bool
	Ready        bool
	lastInput    time.Time
	lastIdx      int
	intervals    []int64
}

type Manager struct {
	rooms  sync.Map
	queues map[string][]*Client
	qMu    sync.Mutex
	db     *db.DB
	log    *slog.Logger
	done   chan struct{}
}

type LobbyInfo struct {
	ID      string `json:"id"`
	Players int    `json:"players"`
	Status  string `json:"status"`
}

func New(d *db.DB, l *slog.Logger) *Manager {
	m := &Manager{
		queues: make(map[string][]*Client),
		db:     d,
		log:    l,
		done:   make(chan struct{}),
	}
	go m.matchmaker()
	return m
}

func (m *Manager) Shutdown() {
	close(m.done)
}

func (m *Manager) CreateRoom(owner, mode string, s Settings) string {
	id := genID()
	r := &Room{
		ID: id, Owner: owner, Mode: mode, Settings: s,
		clients: make(map[string]*Client), db: m.db, log: m.log,
		broadcast: make(chan any, 256), unregister: make(chan string),
		input: make(chan *inputMsg, 64),
		ChatHistory: make([]map[string]any, 0),
	}
	m.rooms.Store(id, r)
	go r.run(func() { m.rooms.Delete(id) })
	return id
}

func (m *Manager) broadcastLobbyPlayers(queueKey string) {
	m.qMu.Lock()
	clients, ok := m.queues[queueKey]
	if !ok {
		m.qMu.Unlock()
		return
	}

	playersList := make([]any, 0, len(clients))
	for _, c := range clients {
		playersList = append(playersList, map[string]any{
			"user_id":  c.ID,
			"username": c.Username,
			"rating":   c.Rating,
		})
	}
	m.qMu.Unlock()

	msg := map[string]any{
		"type":    "player_joined",
		"payload": playersList,
	}

	m.qMu.Lock()
	for _, c := range m.queues[queueKey] {
		select {
		case c.send <- msg:
		default:
		}
	}
	m.qMu.Unlock()
}

func (m *Manager) HandleWS(w http.ResponseWriter, r *http.Request, uid, user string) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
	if err != nil {
		m.log.Warn("ошибка рукопожатия", "err", err)
		return
	}

	rid := r.URL.Query().Get("room_id")

	if rid == "" {
		u, _ := m.db.GetUser(r.Context(), uid)
		rating := 1000
		if u != nil {
			rating = u.Rating
		}
		client := &Client{
			ID:       uid,
			Username: user,
			Rating:   rating,
			conn:     c,
			joinTime: time.Now(),
			send:     make(chan any, 64),
		}

		var msg struct {
			Type    string `json:"type"`
			Payload struct{ Mode, Language, TextMode string } `json:"payload"`
		}

		ctx, cancel := context.WithTimeout(r.Context(), time.Second*5)
		if err := wsjson.Read(ctx, c, &msg); err != nil {
			cancel()
			_ = c.Close(websocket.StatusProtocolError, "ошибка инициализации")
			return
		}
		cancel()

		m.qMu.Lock()
		k := msg.Payload.Language + "|" + msg.Payload.TextMode
		m.queues[k] = append(m.queues[k], client)
		m.qMu.Unlock()

		go client.writeLoop()
		m.broadcastLobbyPlayers(k)
		go m.lobbyReadLoop(client, k)
		return
	}
	val, ok := m.rooms.Load(rid)
	if !ok {
		_ = c.Close(websocket.StatusNormalClosure, "комната не найдена")
		return
	}
	room := val.(*Room)

	room.mu.Lock()
	_, isReconnecting := room.clients[uid]
	if !isReconnecting && len(room.clients) >= room.Settings.MaxPlayers {
		room.mu.Unlock()
		_ = c.Close(websocket.StatusPolicyViolation, "LOBBY_FULL")
		return
	}

	if isReconnecting {
		oldClient, exists := room.clients[uid]
		if exists {
			_ = oldClient.conn.Close(websocket.StatusGoingAway, "reconnected")
			delete(room.clients, uid)
		}
	}
	room.mu.Unlock()

	if user == "" {
		user = "Unknown_Agent_" + uid[:4]
	}

	cl := &Client{
		ID:        uid,
		Username:  user,
		conn:      c,
		room:      room,
		joinTime:  time.Now(),
		lastInput: time.Now(),
		send:      make(chan any, 64),
	}
	room.join(cl)
	go cl.writeLoop()
	go cl.readLoop()
}

func (m *Manager) lobbyReadLoop(c *Client, queueKey string) {
	defer func() {
		m.qMu.Lock()
		q := m.queues[queueKey]
		for i, cl := range q {
			if cl == c {
				m.queues[queueKey] = append(q[:i], q[i+1:]...)
				break
			}
		}
		m.qMu.Unlock()
		m.broadcastLobbyPlayers(queueKey)
		_ = c.conn.Close(websocket.StatusNormalClosure, "")
	}()

	for {
		var msg struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := wsjson.Read(context.Background(), c.conn, &msg); err != nil {
			break
		}
		if msg.Type == "chat_message" {
			var p struct {
				Text string `json:"text"`
			}
			if json.Unmarshal(msg.Payload, &p) == nil {
				out := map[string]any{
					"type": "chat_message",
					"payload": map[string]any{
						"sender_name": c.Username,
						"text":        p.Text,
						"time":        time.Now(),
					},
				}
				m.qMu.Lock()
				for _, recipient := range m.queues[queueKey] {
					select {
					case recipient.send <- out:
					default:
					}
				}
				m.qMu.Unlock()
			}
		}
	}
}

func (m *Manager) matchmaker() {
	ticker := time.NewTicker(MatchmakerTick)
	defer ticker.Stop()
	for {
		select {
		case <-m.done:
			return
		case <-ticker.C:
			m.qMu.Lock()
			for k, q := range m.queues {
				if len(q) < 2 {
					continue
				}

				parts := strings.Split(k, "|")
				lang, textMode := parts[0], parts[1]

				sort.Slice(q, func(i, j int) bool { return q[i].Rating < q[j].Rating })

				var next []*Client
				matched := make([]bool, len(q))

				for i := 0; i < len(q)-1; i++ {
					if matched[i] {
						continue
					}
					p1, p2 := q[i], q[i+1]
					diff := math.Abs(float64(p1.Rating - p2.Rating))
					wait := time.Since(p1.joinTime).Seconds()

					if diff <= MatchmakingBaseRange+wait*MatchmakingTimeMult {
						matched[i], matched[i+1] = true, true
						go m.startMatch(p1, p2, lang, textMode)
					}
				}

				for i, c := range q {
					if !matched[i] {
						next = append(next, c)
					}
				}
				m.queues[k] = next
			}
			m.qMu.Unlock()
		}
	}
}

func (m *Manager) startMatch(p1, p2 *Client, lang, textMode string) {
	rid := m.CreateRoom(p1.ID, "matchmaking", Settings{
		MaxPlayers: 2, Language: lang, TextMode: textMode, Category: "general",
	})

	msg := map[string]any{"type": "match_found", "payload": map[string]string{"room_id": rid}}

	p1.send <- msg
	p2.send <- msg
}

func (m *Manager) CreateManualLobby(ownerID string) string {
	id := genID()

	r := &Room{
		ID:    id,
		Owner: ownerID,
		Mode:  "private",
		State: StateLobby,
		Settings: Settings{
			MaxPlayers: 3,
			Language:   "ru",
			TextMode:   "standard",
		},
		clients:     make(map[string]*Client),
		db:          m.db,
		log:         m.log,
		broadcast:   make(chan any, 256),
		unregister:  make(chan string),
		input:       make(chan *inputMsg, 64),
		ChatHistory: make([]map[string]any, 0),
	}

	m.rooms.Store(id, r)
	go r.run(func() { m.rooms.Delete(id) })
	return id
}

type Room struct {
	ID, Owner, Mode string
	Settings        Settings
	State           int
	Text            *db.Text
	StartTime       time.Time
	clients         map[string]*Client
	participants    []*Client
	mu              sync.RWMutex
	db              *db.DB
	log             *slog.Logger
	broadcast       chan any
	unregister      chan string
	input           chan *inputMsg
	ChatHistory     []map[string]any
}

type inputMsg struct {
	c   *Client
	idx int
}

func (r *Room) run(cleanup func()) {
	defer cleanup()
	ticker := time.NewTicker(RoomUpdateTick)
	idle := time.NewTimer(RoomIdleTimeout)
	defer ticker.Stop()

	for {
		select {
		case msg := <-r.broadcast:
			r.mu.RLock()
			for _, c := range r.clients {
				select {
				case c.send <- msg:
				default:
				}
			}
			r.mu.RUnlock()
		case uid := <-r.unregister:
			r.mu.Lock()
			if c, ok := r.clients[uid]; ok {
				delete(r.clients, uid)
				close(c.send)
			}
			empty := len(r.clients) == 0
			r.mu.Unlock()

			if empty {
				go func() {
					time.Sleep(5 * time.Second)
					r.mu.RLock()
					if len(r.clients) == 0 {
						r.mu.RUnlock()
						cleanup()
					} else {
						r.mu.RUnlock()
					}
				}()
			} else {
				r.sendPlayers()
			}
		case in := <-r.input:
			r.handleInput(in.c, in.idx)
		case <-ticker.C:
			r.mu.RLock()
			if r.State == StateGame {
				list := make([]any, 0, len(r.clients))
				for _, c := range r.clients {
					c.mu.Lock()
					list = append(list, map[string]any{"user_id": c.ID, "progress": c.Progress, "wpm": int(c.WPM)})
					c.mu.Unlock()
				}
				r.mu.RUnlock()
				r.broadcast <- map[string]any{"type": "state_update", "payload": list}
			} else {
				r.mu.RUnlock()
			}
		case <-idle.C:
			return
		}
	}
}

func (r *Room) join(c *Client) {
	r.mu.Lock()
	fmt.Printf("--- Player %s (%s) joining room %s ---\n", c.Username, c.ID, r.ID)

	if len(r.clients) >= r.Settings.MaxPlayers {
		r.mu.Unlock()
		_ = c.conn.Close(websocket.StatusPolicyViolation, "Lobby is full")
		return
	}

	r.clients[c.ID] = c
	if len(r.ChatHistory) > 0 {
		c.send <- map[string]any{
			"type":    "chat_history",
			"payload": r.ChatHistory,
		}
	}
	r.mu.Unlock()
	c.send <- map[string]any{"type": "update_settings", "payload": r.Settings}
	r.sendPlayers()
}

func (r *Room) sendPlayers() {
	r.mu.RLock()
	ownerID := r.Owner
	list := make([]any, 0, len(r.clients))
	for _, c := range r.clients {
		c.mu.Lock()
		list = append(list, map[string]any{
			"user_id":  c.ID,
			"username": c.Username,
			"finished": c.Finished,
			"is_ready": c.Ready,
			"is_owner": c.ID == ownerID,
		})
		c.mu.Unlock()
	}
	r.mu.RUnlock()
	r.broadcast <- map[string]any{"type": "player_joined", "payload": list}
}

func (c *Client) writeLoop() {
	for msg := range c.send {
		ctx, cancel := context.WithTimeout(context.Background(), WriteWait)
		err := wsjson.Write(ctx, c.conn, msg)
		cancel()
		if err != nil {
			return
		}
	}
}

func (c *Client) readLoop() {
	defer func() { c.room.unregister <- c.ID }()
	for {
		var msg struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := wsjson.Read(context.Background(), c.conn, &msg); err != nil {
			break
		}

		switch msg.Type {
		case "update_settings":
    if c.ID != c.room.Owner {
        continue
    }
    var newSettings struct {
        MaxPlayers int    `json:"max_players"`
        Language   string `json:"language"`
    }
    
    if err := json.Unmarshal(msg.Payload, &newSettings); err == nil {
        c.room.mu.Lock()
        if newSettings.MaxPlayers >= 1 && newSettings.MaxPlayers <= 8 {
            c.room.Settings.MaxPlayers = newSettings.MaxPlayers
        }
        if newSettings.Language != "" {
            c.room.Settings.Language = newSettings.Language
        }
        
        currentSettings := c.room.Settings 
        c.room.mu.Unlock()
        c.room.broadcast <- map[string]any{
            "type":    "update_settings",
            "payload": currentSettings,
        }
        c.room.sendPlayers()
    }
		case "client_input":
			var p struct {
				CurrentIndex int `json:"current_index"`
			}
			if json.Unmarshal(msg.Payload, &p) == nil {
				c.room.input <- &inputMsg{c, p.CurrentIndex}
			}
		case "player_ready":
			c.mu.Lock()
			c.Ready = !c.Ready
			c.mu.Unlock()
			if c.room.Mode == "solo" && c.Ready {
				go c.room.startGame()
			}
			c.room.sendPlayers()
		case "game_start":
			go c.room.startGame()
		case "chat_message":
			var p struct {
				Text string `json:"text"`
			}
			if json.Unmarshal(msg.Payload, &p) == nil {
				newMsg := map[string]any{
					"sender_id":   c.ID,
					"sender_name": c.Username,
					"text":        p.Text,
					"time":        time.Now(),
				}

				c.room.mu.Lock()
				c.room.ChatHistory = append(c.room.ChatHistory, newMsg)
				if len(c.room.ChatHistory) > 50 {
					c.room.ChatHistory = c.room.ChatHistory[1:]
				}
				c.room.mu.Unlock()

				c.room.broadcast <- map[string]any{
					"type":    "chat_message",
					"payload": newMsg,
				}
			}
		}
	}
}

func (r *Room) startGame() {
	r.mu.Lock()
	if r.State != StateLobby {
		r.mu.Unlock()
		return
	}
	r.State = StateLoading
	r.mu.Unlock()

	var t *db.Text
	var err error
	if r.Settings.TextMode == "generate" {
		t, err = r.db.GenerateText(context.Background(), r.Settings.Language)
	} else {
		t, err = r.db.GetText(context.Background(), r.Settings.Language, r.Settings.Category, r.Settings.TextID)
	}

	if err != nil {
		r.mu.Lock()
		r.State = StateLobby
		r.mu.Unlock()
		return
	}

	r.mu.Lock()
	r.Text = t
	r.State = StateGame
	r.StartTime = time.Now().Add(StartDelay)
	r.participants = make([]*Client, 0, len(r.clients))
	for _, c := range r.clients {
		c.mu.Lock()
		c.Progress, c.WPM, c.Finished, c.Disqualified, c.lastIdx, c.lastInput, c.intervals = 0, 0, false, false, 0, r.StartTime, nil
		c.mu.Unlock()
		r.participants = append(r.participants, c)
	}
	r.mu.Unlock()

	r.broadcast <- map[string]any{"type": "game_start", "payload": map[string]any{"text": t.Content, "start_time": r.StartTime}}
}

func (r *Room) handleInput(c *Client, idx int) {
	r.mu.RLock()
	if r.State != StateGame || time.Now().Before(r.StartTime) {
		r.mu.RUnlock()
		return
	}
	r.mu.RUnlock()

	c.mu.Lock()
	if c.Disqualified || idx <= c.lastIdx {
		c.mu.Unlock()
		return
	}

	c.lastIdx, c.Progress = idx, float64(idx)
	if m := time.Since(r.StartTime).Minutes(); m > 0 {
		c.WPM = (float64(idx) / WPMCharCount) / m
	}
	c.Finished = idx >= r.Text.Length
	finished := c.Finished
	c.mu.Unlock()

	if finished {
		r.mu.RLock()
		all := true
		for _, p := range r.participants {
			p.mu.Lock()
			if !p.Finished {
				all = false
			}
			p.mu.Unlock()
		}
		r.mu.RUnlock()
		if all {
			r.finish()
		}
	}
}

func (r *Room) finish() {
	r.mu.Lock()
	if r.State == StateFinished {
		r.mu.Unlock()
		return
	}
	r.State = StateFinished

	res := make([]db.MatchResult, 0, len(r.participants))
	for _, c := range r.participants {
		c.mu.Lock()
		wpm := int(c.WPM)
		if c.Disqualified {
			wpm = 0
		}
		res = append(res, db.MatchResult{UserID: c.ID, WPM: wpm, Accuracy: 100})
		c.mu.Unlock()
	}
	r.mu.Unlock()

	sort.Slice(res, func(i, j int) bool { return res[i].WPM > res[j].WPM })

	states := make([]any, len(res))
	for i := range res {
		res[i].Rank = i + 1
		change := 0
		if r.Mode == "matchmaking" && len(res) >= 2 {
			change = 15
			if i > 0 {
				change = -15
			}
			_ = r.db.UpdateRating(context.Background(), res[i].UserID, change)
		}
		states[i] = map[string]any{"user_id": res[i].UserID, "wpm": res[i].WPM, "finished": true, "rating_change": change}
	}
	_ = r.db.SaveMatch(context.Background(), r.Text.ID, res)
	r.broadcast <- map[string]any{"type": "game_end", "payload": map[string]any{"results": states}}
}

func calculateElo(ra, rb int, score float64) int {
	ea := 1.0 / (1.0 + math.Pow(10.0, float64(rb-ra)/400.0))
	return int(EloKFactor * (score - ea))
}

func genID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (m *Manager) GetActiveLobbies() []LobbyInfo {
	var list []LobbyInfo

	m.rooms.Range(func(key, value any) bool {
		roomID, okID := key.(string)
		room, okRoom := value.(*Room)

		if okID && okRoom {
			count := len(room.clients)

			list = append(list, LobbyInfo{
				ID:      roomID,
				Players: count,
				Status:  "WAITING",
			})
		}
		return true
	})

	return list
}