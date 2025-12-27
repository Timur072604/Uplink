package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"uplink/backend/internal/db"
	"uplink/backend/internal/game"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/time/rate"
)

type ctxKey int

const (
	uidKey ctxKey = iota
	userKey
)

type visitor struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type API struct {
	db      *db.DB
	gm      *game.Manager
	secret  []byte
	origins map[string]bool
	log     *slog.Logger
	limit   sync.Map
}

func New(d *db.DB, g *game.Manager, s string, origins []string, l *slog.Logger) http.Handler {
	fmt.Println(">>> [INIT] Запуск API и инициализация статики...")
	
	allowed := make(map[string]bool)
	for _, o := range origins {
		allowed[strings.TrimSpace(o)] = true
	}

	a := &API{
		db:      d,
		gm:      g,
		secret:  []byte(s),
		origins: allowed,
		log:     l,
	}

	go a.cleanupVisitors()

	mux := http.NewServeMux()
	

	mux.HandleFunc("POST /api/v1/auth/register", a.register)
	mux.HandleFunc("POST /api/v1/auth/login", a.login)

	auth := a.authMiddleware
	mux.HandleFunc("GET /api/v1/users/me", auth(a.me))
	mux.HandleFunc("GET /api/v1/users/history", auth(a.history))
	mux.HandleFunc("GET /api/v1/leaderboard", auth(a.leaderboard))

	mux.HandleFunc("GET /api/v1/lobbies", auth(a.handleGetLobbies))
	mux.HandleFunc("POST /api/v1/lobby/create", auth(a.handleCreateManualLobby))
	mux.HandleFunc("POST /api/v1/practice", auth(a.createRoom("solo")))
	mux.HandleFunc("/ws/lobby/", a.handleLobbyWS)

	mux.HandleFunc("/ws", a.handleWS)

	staticDir := "./frontend/static"
	fs := http.FileServer(http.Dir(staticDir))

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Printf("[HTTP] %s %s | IP: %s\n", r.Method, r.URL.Path, r.RemoteAddr)

		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/ws" {
			http.NotFound(w, r)
			return
		}

		filePath := filepath.Join(staticDir, r.URL.Path)
		fileInfo, err := os.Stat(filePath)

		if os.IsNotExist(err) || (err == nil && fileInfo.IsDir()) {
			fmt.Printf("[DEBUG] Файл не найден: %s. Отдаю Fallback: index.html\n", filePath)
			http.ServeFile(w, r, filepath.Join(staticDir, "index.html"))
			return
		}

		if err != nil {
			fmt.Printf("[ERROR] Ошибка доступа к файлу %s: %v\n", filePath, err)
		}
		fs.ServeHTTP(w, r)
	})

	return a.corsMiddleware(a.rateLimitMiddleware(mux))
}

func (a *API) cleanupVisitors() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		a.limit.Range(func(k, v any) bool {
			if time.Since(v.(*visitor).lastSeen) > 3*time.Minute {
				a.limit.Delete(k)
			}
			return true
		})
	}
}

func (a *API) register(w http.ResponseWriter, r *http.Request) {
	var req struct{ Username, Password string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Password) < 8 {
		a.error(w, "некорректный запрос", 400)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 10)
	if err != nil {
		a.error(w, "ошибка сервера", 500)
		return
	}
	id, err := a.db.CreateUser(r.Context(), req.Username, string(hash))
	if err != nil {
		a.error(w, "пользователь уже существует", 409)
		return
	}
	a.sendToken(w, id, req.Username)
}
func (a *API) handleCreateManualLobby(w http.ResponseWriter, r *http.Request) {
    uid := r.Context().Value(uidKey).(string)
    roomID := a.gm.CreateManualLobby(uid)
    a.json(w, map[string]string{"room_id": roomID}, 200)
}
func (a *API) login(w http.ResponseWriter, r *http.Request) {
	var req struct{ Username, Password string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		a.error(w, "некорректный запрос", 400)
		return
	}
	u, err := a.db.GetUser(r.Context(), req.Username)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(u.Password), []byte(req.Password)) != nil {
		a.error(w, "неверные данные", 401)
		return
	}
	a.sendToken(w, u.ID, u.Username)
}
func (a *API) getLeaderboard(w http.ResponseWriter, r *http.Request) {
    list, err := a.db.GetLeaderboard(r.Context(), 50) 
    if err != nil {
        a.error(w, "ошибка загрузки таблицы", 500)
        return
    }
    a.json(w, list, 200)
}
func (a *API) sendToken(w http.ResponseWriter, id, name string) {
	t, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":      id,
		"username": name,
		"exp":      time.Now().Add(24 * time.Hour).Unix(),
	}).SignedString(a.secret)
	if err != nil {
		a.error(w, "ошибка токена", 500)
		return
	}
	a.json(w, map[string]string{"token": t}, 200)
}

func (a *API) me(w http.ResponseWriter, r *http.Request) {
    uid := r.Context().Value(uidKey).(string)
    
    user, err := a.db.GetUserByID(r.Context(), uid) 
    if err != nil {
        a.error(w, "пользователь не найден", 404)
        return
    }
    a.json(w, user, 200)
}

func (a *API) history(w http.ResponseWriter, r *http.Request) {
	uid, _ := r.Context().Value(uidKey).(string)
	d, next, err := a.db.GetHistory(r.Context(), uid, 20, r.URL.Query().Get("cursor"))
	if err != nil {
		a.error(w, "ошибка бд", 500)
		return
	}
	a.json(w, map[string]any{"data": d, "next_cursor": next}, 200)
}

func (a *API) leaderboard(w http.ResponseWriter, r *http.Request) {
	d, err := a.db.GetLeaderboard(r.Context(), 100)
	if err != nil {
		a.error(w, "ошибка бд", 500)
		return
	}
	a.json(w, map[string]any{"data": d}, 200)
}

func (a *API) createRoom(mode string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var s game.Settings
		if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
			a.error(w, "некорректный запрос", 400)
			return
		}
		if mode == "solo" {
			s.MaxPlayers = 1
		}
		uid, _ := r.Context().Value(uidKey).(string)
		rm := a.gm.CreateRoom(uid, mode, s)
		a.json(w, map[string]string{"room_id": rm}, 200)
	}
}

func (a *API) ws(w http.ResponseWriter, r *http.Request) {
	tokenStr := r.URL.Query().Get("token")
	t, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) { return a.secret, nil })
	if err != nil || !t.Valid {
		a.error(w, "неавторизован", 401)
		return
	}
	c := t.Claims.(jwt.MapClaims)
	sub, _ := c["sub"].(string)
	un, _ := c["username"].(string)
	a.gm.HandleWS(w, r, sub, un)
}

func (a *API) json(w http.ResponseWriter, d any, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(d)
}

func (a *API) error(w http.ResponseWriter, msg string, code int) {
	a.json(w, map[string]string{"error": msg}, code)
}

func (a *API) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			a.error(w, "нет токена", 401)
			return
		}
		tokenStr := strings.TrimPrefix(h, "Bearer ")
		t, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) { return a.secret, nil })
		if err != nil || !t.Valid {
			a.error(w, "неверный токен", 401)
			return
		}
		claims := t.Claims.(jwt.MapClaims)
		ctx := context.WithValue(r.Context(), uidKey, claims["sub"])
		ctx = context.WithValue(ctx, userKey, claims["username"])
		next(w, r.WithContext(ctx))
	}
}

func (a *API) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		allow := a.origins["*"] || a.origins[origin]
		if allow {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if ip == "" {
			ip = r.RemoteAddr
		}
		v, _ := a.limit.LoadOrStore(ip, &visitor{limiter: rate.NewLimiter(rate.Limit(10), 20)})
		vis := v.(*visitor)
		vis.lastSeen = time.Now()
		if !vis.limiter.Allow() {
			a.error(w, "слишком много запросов", 429)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) handleGetLobbies(w http.ResponseWriter, r *http.Request) {
	list := a.gm.GetActiveLobbies()
	a.json(w, list, 200)
}

func (a *API) handleLobbyWS(w http.ResponseWriter, r *http.Request) {

	uid, _ := r.Context().Value(uidKey).(string)
	username, _ := r.Context().Value(userKey).(string)

	a.gm.HandleWS(w, r, uid, username)
}

func (a *API) handleWS(w http.ResponseWriter, r *http.Request) {
	fmt.Printf("[WS_DEBUG] New connection request from IP: %s\n", r.RemoteAddr)

	uid, _ := r.Context().Value(uidKey).(string)
	user, _ := r.Context().Value(userKey).(string)
	
	fmt.Printf("[WS_DEBUG] Context data -> UID: '%s', User: '%s'\n", uid, user)

	if uid == "" {
		tokenStr := r.URL.Query().Get("token")
		if tokenStr == "" {
			fmt.Println("[WS_DEBUG] No token found in URL query parameters")
		} else {
			token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (interface{}, error) {
				return a.secret, nil
			})

			if err != nil {
				fmt.Printf("[WS_DEBUG] JWT Parse Error: %v\n", err)
			} else if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
				if s, ok := claims["sub"].(string); ok { uid = s }
				if u, ok := claims["username"].(string); ok { user = u }
				fmt.Printf("[WS_DEBUG] Token parsed -> UID from token: '%s', User from token: '%s'\n", uid, user)
			} else {
				fmt.Println("[WS_DEBUG] Token is invalid or claims are missing")
			}
		}
	}

	if uid != "" && (user == "" || user == "Guest") {
		fmt.Printf("[WS_DEBUG] Attempting DB lookup for UID: %s\n", uid)
		dbUser, err := a.db.GetUserByID(r.Context(), uid)
		if err != nil {
			fmt.Printf("[WS_DEBUG] DB Error: %v\n", err)
		} else if dbUser != nil {
			fmt.Printf("[WS_DEBUG] DB Found User: %+v\n", dbUser)
			user = dbUser.Username
		} else {
			fmt.Println("[WS_DEBUG] DB returned nil user (not found)")
		}
	}


	if uid == "" {
		uid = fmt.Sprintf("guest_%d", time.Now().UnixNano())
		user = "Guest_" + uid[len(uid)-4:]
		fmt.Printf("[WS_DEBUG] AUTH FAILED. Assigned temporary ID: %s\n", user)
	}

	fmt.Printf("[WS_DEBUG] Final result for Game Manager -> UID: %s, Username: %s\n", uid, user)
	a.gm.HandleWS(w, r, uid, user)
}