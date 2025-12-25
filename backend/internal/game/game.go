package game

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"math"
	"math/big"
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
	EloKFactor           = 32.0

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
	Language, TextMode, Category string
	TextID, MaxPlayers           int
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
	}
	m.rooms.Store(id, r)
	go r.run(func() { m.rooms.Delete(id) })
	return id
}

func (m *Manager) HandleWS(w http.ResponseWriter, r *http.Request, uid, user string) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
	if err != nil {
		m.log.Warn("ошибка рукопожатия", "err", err)
		return
	}

	rid := r.URL.Query().Get("room_id")
	if rid == "" {
		var msg struct {
			Payload struct{ Mode, Language, TextMode string }
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Second*5)
		if err := wsjson.Read(ctx, c, &msg); err != nil {
			cancel()
			_ = c.Close(websocket.StatusProtocolError, "ошибка чтения")
			return
		}
		cancel()

		u, _ := m.db.GetUser(r.Context(), user)
		rating := 1000
		if u != nil {
			rating = u.Rating
		}

		textMode := msg.Payload.TextMode
		if textMode != "generate" {
			textMode = "standard"
		}

		m.qMu.Lock()
		k := msg.Payload.Language + "|" + textMode
		m.queues[k] = append(m.queues[k], &Client{ID: uid, Username: user, Rating: rating, conn: c, joinTime: time.Now(), send: make(chan any, 64)})
		m.qMu.Unlock()
		return
	}

	val, ok := m.rooms.Load(rid)
	if !ok {
		_ = c.Close(websocket.StatusNormalClosure, "комната не найдена")
		return
	}
	room := val.(*Room)

	cl := &Client{ID: uid, Username: user, conn: c, room: room, joinTime: time.Now(), lastInput: time.Now(), send: make(chan any, 64)}
	room.join(cl)

	go cl.writeLoop()
	go cl.readLoop()
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
				if len(parts) != 2 {
					continue
				}
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
	cat := "general"
	if textMode == "standard" {
		cats := []string{"general", "literature", "code"}
		if n, err := rand.Int(rand.Reader, big.NewInt(int64(len(cats)))); err == nil {
			cat = cats[n.Int64()]
		}
	}

	rid := m.CreateRoom(p1.ID, "matchmaking", Settings{
		MaxPlayers: 2,
		Language:   lang,
		TextMode:   textMode,
		Category:   cat,
	})
	msg := map[string]any{"type": "match_found", "payload": map[string]string{"room_id": rid}}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()

	_ = wsjson.Write(ctx, p1.conn, msg)
	_ = wsjson.Write(ctx, p2.conn, msg)
	_ = p1.conn.Close(websocket.StatusNormalClosure, "")
	_ = p2.conn.Close(websocket.StatusNormalClosure, "")
}

type Room struct {
	ID, Owner, Mode string
	Settings        Settings
	State           int
	Text            *db.Text
	StartTime       time.Time

	clients      map[string]*Client
	participants []*Client
	mu           sync.RWMutex
	db           *db.DB
	log          *slog.Logger
	broadcast    chan any
	unregister   chan string
	input        chan *inputMsg
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

				if r.State == StateGame {
					c.mu.Lock()
					c.Disqualified = true
					c.Finished = true
					c.mu.Unlock()
				}
			}

			allFinished := true
			if r.State == StateGame {
				for _, p := range r.participants {
					p.mu.Lock()
					if !p.Finished {
						allFinished = false
					}
					p.mu.Unlock()
				}
			} else {
				allFinished = len(r.clients) == 0
			}

			r.mu.Unlock()

			if r.State == StateGame && allFinished {
				r.finish()
			} else if r.State != StateGame && allFinished {
				return
			}

			if r.State != StateFinished {
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
		r.mu.RLock()
		if len(r.clients) > 0 {
			idle.Reset(RoomIdleTimeout)
		}
		r.mu.RUnlock()
	}
}

func (r *Room) join(c *Client) {
	r.mu.Lock()
	if len(r.clients) >= r.Settings.MaxPlayers {
		r.mu.Unlock()
		_ = c.conn.Close(websocket.StatusPolicyViolation, "комната переполнена")
		return
	}
	r.clients[c.ID] = c
	r.mu.Unlock()

	select {
	case c.send <- map[string]any{"type": "update_settings", "payload": r.Settings}:
	default:
	}
	r.sendPlayers()
}

func (r *Room) sendPlayers() {
	r.mu.RLock()
	list := make([]any, 0, len(r.clients))
	for _, c := range r.clients {
		c.mu.Lock()
		list = append(list, map[string]any{"user_id": c.ID, "username": c.Username, "finished": c.Finished, "is_ready": c.Ready})
		c.mu.Unlock()
	}
	r.mu.RUnlock()
	r.broadcast <- map[string]any{"type": "player_joined", "payload": list}
}

func (c *Client) writeLoop() {
	defer func() {
		_ = c.conn.Close(websocket.StatusNormalClosure, "")
	}()
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
	c.conn.SetReadLimit(32768)

	for {
		var msg struct {
			Type    string
			Payload json.RawMessage
		}
		if err := wsjson.Read(context.Background(), c.conn, &msg); err != nil {
			break
		}

		switch msg.Type {
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
			ready := c.Ready
			c.mu.Unlock()
			if c.room.Mode == "solo" && ready {
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
				c.room.broadcast <- map[string]any{"type": "chat_message", "payload": map[string]any{"sender_id": c.ID, "sender_name": c.Username, "text": p.Text, "time": time.Now()}}
			}
		case "update_settings":
			var s Settings
			if json.Unmarshal(msg.Payload, &s) == nil && c.ID == c.room.Owner {
				c.room.mu.RLock()
				if c.room.State == StateLobby {
					c.room.mu.RUnlock()
					c.room.mu.Lock()
					c.room.Settings = s
					c.room.mu.Unlock()
					c.room.broadcast <- map[string]any{"type": "update_settings", "payload": s}
				} else {
					c.room.mu.RUnlock()
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
		r.log.Error("ошибка получения текста", "err", err)
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
	if r.State != StateGame {
		r.mu.RUnlock()
		return
	}
	if time.Now().Before(r.StartTime) {
		r.mu.RUnlock()
		return
	}
	r.mu.RUnlock()

	c.mu.Lock()
	if c.Disqualified || idx <= c.lastIdx {
		c.mu.Unlock()
		return
	}
	now := time.Now()
	diff := now.Sub(c.lastInput).Milliseconds()
	c.intervals = append(c.intervals, diff)
	if len(c.intervals) > InputBufferSize {
		c.intervals = c.intervals[1:]
	}

	var sum, sqSum float64
	for _, v := range c.intervals {
		val := float64(v)
		sum += val
		sqSum += val * val
	}
	mean := sum / float64(len(c.intervals))
	variance := (sqSum / float64(len(c.intervals))) - (mean * mean)

	if len(c.intervals) > MinInputSamples && (mean < AntiCheatMinMean || variance < AntiCheatMinVariance) {
		c.Disqualified = true
		c.Finished = true
		c.mu.Unlock()
		_ = c.conn.Close(websocket.StatusPolicyViolation, "обнаружен чит")
		return
	}

	c.lastIdx, c.lastInput, c.Progress = idx, now, float64(idx)
	if m := time.Since(r.StartTime).Minutes(); m > 0 {
		c.WPM = (float64(idx) / WPMCharCount) / m
	}
	finished := idx >= r.Text.Length
	c.Finished = finished
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
			if !all {
				break
			}
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

		if r.Mode == "matchmaking" && len(res) == 2 {
			p1 := res[0]
			p2 := res[1]

			u1, _ := r.db.GetUser(context.Background(), p1.UserID)
			u2, _ := r.db.GetUser(context.Background(), p2.UserID)

			if u1 != nil && u2 != nil {
				score := 1.0
				if i == 1 {
					score = 0.0
				}

				myRating := u1.Rating
				oppRating := u2.Rating
				if i == 1 {
					myRating, oppRating = u2.Rating, u1.Rating
				}

				change = calculateElo(myRating, oppRating, score)
				if err := r.db.UpdateRating(context.Background(), res[i].UserID, change); err != nil {
					r.log.Error("ошибка обновления рейтинга", "err", err)
				}
			}
		}
		states[i] = map[string]any{"user_id": res[i].UserID, "wpm": res[i].WPM, "finished": true, "rating_change": change}
	}
	if err := r.db.SaveMatch(context.Background(), r.Text.ID, res); err != nil {
		r.log.Error("ошибка сохранения матча", "err", err)
	}
	r.broadcast <- map[string]any{"type": "game_end", "payload": map[string]any{"results": states}}
}

func calculateElo(ra, rb int, score float64) int {
	ea := 1.0 / (1.0 + math.Pow(10.0, float64(rb-ra)/400.0))
	return int(EloKFactor * (score - ea))
}

func genID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "0000"
	}
	return hex.EncodeToString(b)
}
