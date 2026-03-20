package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	_ "modernc.org/sqlite"
)

const authorizationCookieName = "authorization"

type User struct {
	ID       uint   `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Phone    string `json:"phone"`
	Password string `json:"-"`
	Balance  int64  `json:"balance"`
	IsAdmin  bool   `json:"is_admin"`
}

type RegisterRequest struct {
	Username string `json:"username"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Phone    string `json:"phone"`
	Password string `json:"password"`
}

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type WithdrawAccountRequest struct {
	Password string `json:"password"`
}

type UserResponse struct {
	ID       uint   `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Phone    string `json:"phone"`
	Balance  int64  `json:"balance"`
	IsAdmin  bool   `json:"is_admin"`
}

type LoginResponse struct {
	AuthMode string       `json:"auth_mode"`
	Token    string       `json:"token"`
	User     UserResponse `json:"user"`
}

type PostView struct {
	ID          uint   `json:"id"`
	Title       string `json:"title"`
	Content     string `json:"content"`
	OwnerID     uint   `json:"owner_id"`
	Author      string `json:"author"`
	AuthorEmail string `json:"author_email"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type CreatePostRequest struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

type UpdatePostRequest struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

type PostListResponse struct {
	Posts []PostView `json:"posts"`
}

type PostResponse struct {
	Post PostView `json:"post"`
}

type DepositRequest struct {
	Amount int64 `json:"amount"`
}

type BalanceWithdrawRequest struct {
	Amount int64 `json:"amount"`
}

type TransferRequest struct {
	ToUsername string `json:"to_username"`
	Amount     int64  `json:"amount"`
}

type Store struct {
	db *sql.DB
}

type SessionStore struct {
	tokens map[string]User
}

func main() {
	store, err := openStore("./app.db", "./schema.sql", "./seed.sql")
	if err != nil {
		panic(err)
	}
	defer store.close()

	sessions := newSessionStore()

	router := gin.Default()
	registerStaticRoutes(router)

	auth := router.Group("/api/auth")
	{
		auth.POST("/register", func(c *gin.Context) {
			var request RegisterRequest
			if err := c.ShouldBindJSON(&request); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"message": "invalid register request"})
				return
			}

			_, exists, err := store.findUserByUsername(request.Username)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"message": "failed to check username"})
				return
			}
			if exists {
				c.JSON(http.StatusConflict, gin.H{"message": "same username"})
				return
			}

			user, err := store.createUser(request)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"message": "failed to create new user"})
				return
			}
			c.JSON(http.StatusCreated, gin.H{"user": makeUserResponse(user)})

		})

		auth.POST("/login", func(c *gin.Context) {
			var request LoginRequest
			if err := c.ShouldBindJSON(&request); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"message": "invalid login request"})
				return
			}

			user, ok, err := store.findUserByUsername(request.Username)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"message": "failed to load user"})
				return
			}
			if !ok || user.Password != request.Password {
				c.JSON(http.StatusUnauthorized, gin.H{"message": "invalid credentials"})
				return
			}

			token, err := sessions.create(user)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"message": "failed to create session"})
				return
			}

			c.SetSameSite(http.SameSiteLaxMode)
			c.SetCookie(authorizationCookieName, token, 60*60*8, "/", "", false, true)
			c.JSON(http.StatusOK, LoginResponse{
				AuthMode: "header-and-cookie",
				Token:    token,
				User:     makeUserResponse(user),
			})
		})

		auth.POST("/logout", func(c *gin.Context) {
			token := tokenFromRequest(c)
			if token == "" {
				c.JSON(http.StatusUnauthorized, gin.H{"message": "missing authorization token"})
				return
			}
			if _, ok := sessions.lookup(token); !ok {
				c.JSON(http.StatusUnauthorized, gin.H{"message": "invalid authorization token"})
				return
			}

			sessions.delete(token)
			clearAuthorizationCookie(c)
			c.JSON(http.StatusOK, gin.H{
				"message": "dummy logout handler",
				"todo":    "replace with revoke or audit logic if needed",
			})
		})

		auth.POST("/withdraw", func(c *gin.Context) {
			var request WithdrawAccountRequest
			if err := c.ShouldBindJSON(&request); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"message": "invalid withdraw request"})
				return
			}

			token := tokenFromRequest(c)
			if token == "" {
				c.JSON(http.StatusUnauthorized, gin.H{"message": "missing authorization token"})
				return
			}
			user, ok := sessions.lookup(token)
			if !ok {
				c.JSON(http.StatusUnauthorized, gin.H{"message": "invalid authorization token"})
				return
			}

			if err := store.deleteUser(user.Username); err != nil {
				c.JSON(http.StatusUnauthorized, gin.H{"message": "delete user fail"})
				return
			}
			sessions.delete(token)
			clearAuthorizationCookie(c)

			c.JSON(http.StatusAccepted, gin.H{
				"message": "dummy withdraw handler",
				"todo":    "replace with password check and account delete logic",
				"user":    makeUserResponse(user),
			})
		})
	}

	protected := router.Group("/api")
	{
		protected.GET("/me", func(c *gin.Context) {
			token := tokenFromRequest(c)
			if token == "" {
				c.JSON(http.StatusUnauthorized, gin.H{"message": "missing authorization token"})
				return
			}
			user, ok := sessions.lookup(token)
			if !ok {
				c.JSON(http.StatusUnauthorized, gin.H{"message": "invalid authorization token"})
				return
			}

			c.JSON(http.StatusOK, gin.H{"user": makeUserResponse(user)})
		})

		protected.POST("/banking/deposit", func(c *gin.Context) {
			var request DepositRequest
			if err := c.ShouldBindJSON(&request); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"message": "invalid deposit request"})
				return
			}

			token := tokenFromRequest(c)
			if token == "" {
				c.JSON(http.StatusUnauthorized, gin.H{"message": "missing authorization token"})
				return
			}

			user, ok := sessions.lookup(token)
			if !ok {
				c.JSON(http.StatusUnauthorized, gin.H{"message": "invalid authorization token"})
				return
			}

			if request.Amount <= 0 {
				c.JSON(http.StatusBadRequest, gin.H{"message": "amount positive"})
				return
			}

			updated, err := store.deposit(user.ID, request.Amount)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"message": "failed to deposit"})
				return
			}

			c.JSON(http.StatusOK, gin.H{
				"message": "dummy deposit handler",
				"todo":    "replace with balance increment query",
				"user":    makeUserResponse(updated),
				"amount":  request.Amount,
			})
		})

		protected.POST("/banking/withdraw", func(c *gin.Context) {
			var request BalanceWithdrawRequest
			if err := c.ShouldBindJSON(&request); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"message": "invalid withdraw request"})
				return
			}

			token := tokenFromRequest(c)
			if token == "" {
				c.JSON(http.StatusUnauthorized, gin.H{"message": "missing authorization token"})
				return
			}
			user, ok := sessions.lookup(token)
			if !ok {
				c.JSON(http.StatusUnauthorized, gin.H{"message": "invalid authorization token"})
				return
			}
			if !ok {
				c.JSON(http.StatusUnauthorized, gin.H{"message": "invalid authorization token"})
				return
			}

			if user.Balance < request.Amount {
				c.JSON(http.StatusBadRequest, gin.H{"message": "no money"})
				return
			}

			c.JSON(http.StatusOK, gin.H{
				"message": "잔액",
				"todo":    user.Balance - request.Amount,
				"user":    makeUserResponse(user),
				"amount":  request.Amount,
			})
		})

		protected.POST("/banking/transfer", func(c *gin.Context) {
			var request TransferRequest
			if err := c.ShouldBindJSON(&request); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"message": "invalid transfer request"})
				return
			}

			token := tokenFromRequest(c)
			if token == "" {
				c.JSON(http.StatusUnauthorized, gin.H{"message": "missing authorization token"})
				return
			}
			user, ok := sessions.lookup(token)
			if !ok {
				c.JSON(http.StatusUnauthorized, gin.H{"message": "invalid authorization token"})
				return
			}

			c.JSON(http.StatusOK, gin.H{
				"message": "dummy transfer handler",
				"todo":    "replace with transfer transaction and balance checks",
				"user":    makeUserResponse(user),
				"target":  request.ToUsername,
				"amount":  request.Amount,
			})
		})

		protected.GET("/posts", func(c *gin.Context) {
			token := tokenFromRequest(c)
			if token == "" {
				c.JSON(http.StatusUnauthorized, gin.H{"message": "missing authorization token"})
				return
			}
			if _, ok := sessions.lookup(token); !ok {
				c.JSON(http.StatusUnauthorized, gin.H{"message": "invalid authorization token"})
				return
			}

			posts, err := store.findAllPosts()
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"message": "failed to load"})
				return
			}
			c.JSON(http.StatusOK, PostListResponse{Posts: posts})
		})

		protected.POST("/posts", func(c *gin.Context) {
			var request CreatePostRequest
			if err := c.ShouldBindJSON(&request); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"message": "invalid create request"})
				return
			}

			token := tokenFromRequest(c)
			if token == "" {
				c.JSON(http.StatusUnauthorized, gin.H{"message": "missing authorization token"})
				return
			}
			user, ok := sessions.lookup(token)
			if !ok {
				c.JSON(http.StatusUnauthorized, gin.H{"message": "invalid authorization token"})
				return
			}

			post, err := store.createPost(user.ID, request)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"message": "failed to create post"})
				return
			}
			c.JSON(http.StatusCreated, PostResponse{Post: post})
		})

		protected.GET("/posts/:id", func(c *gin.Context) {
			token := tokenFromRequest(c)
			if token == "" {
				c.JSON(http.StatusUnauthorized, gin.H{"message": "missing authorization token"})
				return
			}
			if _, ok := sessions.lookup(token); !ok {
				c.JSON(http.StatusUnauthorized, gin.H{"message": "invalid authorization token"})
				return
			}

			idStr := c.Param("id")
			var id uint
			if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"message": "invalid post id"})
				return
			}

			post, ok, err := store.findPostByID(id)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"message": "failed to load post"})
				return
			}
			if !ok {
				c.JSON(http.StatusNotFound, gin.H{"message": "post not found"})
				return
			}
			c.JSON(http.StatusOK, PostResponse{Post: post})
		})

		protected.PUT("/posts/:id", func(c *gin.Context) {
			var request UpdatePostRequest
			if err := c.ShouldBindJSON(&request); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"message": "invalid update request"})
				return
			}

			token := tokenFromRequest(c)
			if token == "" {
				c.JSON(http.StatusUnauthorized, gin.H{"message": "missing authorization token"})
				return
			}
			user, ok := sessions.lookup(token)
			if !ok {
				c.JSON(http.StatusUnauthorized, gin.H{"message": "invalid authorization token"})
				return
			}

			now := time.Now().Format(time.RFC3339)
			c.JSON(http.StatusOK, gin.H{
				"message": "dummy update post handler",
				"todo":    "replace with ownership check and update query",
				"post": PostView{
					ID:          1,
					Title:       strings.TrimSpace(request.Title),
					Content:     strings.TrimSpace(request.Content),
					OwnerID:     user.ID,
					Author:      user.Name,
					AuthorEmail: user.Email,
					CreatedAt:   "2026-03-19T09:00:00Z",
					UpdatedAt:   now,
				},
			})
		})

		protected.DELETE("/posts/:id", func(c *gin.Context) {
			token := tokenFromRequest(c)
			if token == "" {
				c.JSON(http.StatusUnauthorized, gin.H{"message": "missing authorization token"})
				return
			}
			if _, ok := sessions.lookup(token); !ok {
				c.JSON(http.StatusUnauthorized, gin.H{"message": "invalid authorization token"})
				return
			}

			c.JSON(http.StatusOK, gin.H{
				"message": "dummy delete post handler",
				"todo":    "replace with ownership check and delete query",
			})
		})
	}

	if err := router.Run(":8080"); err != nil {
		panic(err)
	}
}

func openStore(databasePath, schemaFile, seedFile string) (*Store, error) {
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(1)

	store := &Store{db: db}
	if err := store.initialize(schemaFile, seedFile); err != nil {
		_ = db.Close()
		return nil, err
	}

	return store, nil
}

func (s *Store) close() error {
	return s.db.Close()
}

func (s *Store) initialize(schemaFile, seedFile string) error {
	if err := s.execSQLFile(schemaFile); err != nil {
		return err
	}
	if err := s.execSQLFile(seedFile); err != nil {
		return err
	}
	return nil
}

func (s *Store) execSQLFile(path string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(string(content))
	return err
}

func (s *Store) findUserByUsername(username string) (User, bool, error) {
	row := s.db.QueryRow(`
		SELECT id, username, name, email, phone, password, balance, is_admin
		FROM users
		WHERE username = ?
	`, strings.TrimSpace(username))

	var user User
	var isAdmin int64
	if err := row.Scan(&user.ID, &user.Username, &user.Name, &user.Email, &user.Phone, &user.Password, &user.Balance, &isAdmin); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return User{}, false, nil
		}
		return User{}, false, err
	}
	user.IsAdmin = isAdmin == 1

	return user, true, nil
}

func (s *Store) createUser(req RegisterRequest) (User, error) {
	result, err := s.db.Exec(`
        INSERT INTO users (username, name, email, phone, password, balance, is_admin)
        VALUES (?, ?, ?, ?, ?, 0, 0)
    `, strings.TrimSpace(req.Username),
		strings.TrimSpace(req.Name),
		strings.TrimSpace(req.Email),
		strings.TrimSpace(req.Phone),
		req.Password)
	if err != nil {
		return User{}, err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return User{}, err
	}

	return User{
		ID:       uint(id),
		Username: strings.TrimSpace(req.Username),
		Name:     strings.TrimSpace(req.Name),
		Email:    strings.TrimSpace(req.Email),
		Phone:    strings.TrimSpace(req.Phone),
		Balance:  0,
		IsAdmin:  false,
	}, nil
}

func (s *Store) deleteUser(username string) error {
	_, err := s.db.Exec(`
        DELETE FROM users
        WHERE username = ?
    `, strings.TrimSpace(username))
	return err
}

func newSessionStore() *SessionStore {
	return &SessionStore{
		tokens: make(map[string]User),
	}
}

func (s *SessionStore) create(user User) (string, error) {
	token, err := newSessionToken()
	if err != nil {
		return "", err
	}

	s.tokens[token] = user
	return token, nil
}

func (s *SessionStore) lookup(token string) (User, bool) {
	user, ok := s.tokens[token]
	return user, ok
}

func (s *SessionStore) delete(token string) {
	delete(s.tokens, token)
}

// fe 페이지 캐싱으로 테스트에 혼동이 있어, 별도 처리없이 main에 두시면 될 것 같습니다
// registerStaticRoutes 는 정적 파일(HTML, JS, CSS)을 제공하는 라우트를 등록한다.
func registerStaticRoutes(router *gin.Engine) {
	// 브라우저 캐시 비활성화 — 정적 파일과 루트 경로에만 적용
	router.Use(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/static/") || c.Request.URL.Path == "/" {
			c.Header("Cache-Control", "no-cache, no-store, must-revalidate")
			c.Header("Pragma", "no-cache")
			c.Header("Expires", "0")
		}
		c.Next()
	})
	router.Static("/static", "./static")
	router.GET("/", func(c *gin.Context) {
		c.File("./static/index.html")
	})
}

func makeUserResponse(user User) UserResponse {
	return UserResponse{
		ID:       user.ID,
		Username: user.Username,
		Name:     user.Name,
		Email:    user.Email,
		Phone:    user.Phone,
		Balance:  user.Balance,
		IsAdmin:  user.IsAdmin,
	}
}

func clearAuthorizationCookie(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(authorizationCookieName, "", -1, "/", "", false, true)
}

func tokenFromRequest(c *gin.Context) string {
	headerValue := strings.TrimSpace(c.GetHeader("Authorization"))
	if headerValue != "" {
		return headerValue
	}

	cookieValue, err := c.Cookie(authorizationCookieName)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cookieValue)
}

func newSessionToken() (string, error) {
	buffer := make([]byte, 24)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func (s *Store) findAllPosts() ([]PostView, error) {
	rows, err := s.db.Query(`
		SELECT p.id, p.title, p.content, p.owner_id, u.name, u.email, p.created_at, p.updated_at
		FROM posts p
		JOIN users u ON p.owner_id = u.id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var posts []PostView
	for rows.Next() {
		var p PostView
		if err := rows.Scan(&p.ID, &p.Title, &p.Content, &p.OwnerID, &p.Author, &p.AuthorEmail, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		posts = append(posts, p)
	}
	return posts, nil
}

func (s *Store) findPostByID(id uint) (PostView, bool, error) {
	row := s.db.QueryRow(`
		SELECT p.id, p.title, p.content, p.owner_id, u.name, u.email, p.created_at, p.updated_at
		FROM posts p
		JOIN users u ON p.owner_id = u.id
		WHERE p.id = ?
	`, id)

	var p PostView
	if err := row.Scan(&p.ID, &p.Title, &p.Content, &p.OwnerID, &p.Author, &p.AuthorEmail, &p.CreatedAt, &p.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PostView{}, false, nil
		}
		return PostView{}, false, err
	}
	return p, true, nil
}

func (s *Store) createPost(ownerID uint, req CreatePostRequest) (PostView, error) {
	result, err := s.db.Exec(`
		INSERT INTO posts (title, content, owner_id)
		VALUES (?, ?, ?)
	`, strings.TrimSpace(req.Title), strings.TrimSpace(req.Content), ownerID)
	if err != nil {
		return PostView{}, err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return PostView{}, err
	}

	post, _, err := s.findPostByID(uint(id))
	return post, err
}

func (s *Store) updatePost(id uint, ownerID uint, req UpdatePostRequest) (PostView, error) {
	_, err := s.db.Exec(`
		UPDATE posts
		SET title = ?, content = ?, updated_at = datetime('now')
		WHERE id = ? AND owner_id = ?
	`, strings.TrimSpace(req.Title), strings.TrimSpace(req.Content), id, ownerID)
	if err != nil {
		return PostView{}, err
	}

	post, _, err := s.findPostByID(id)
	return post, err
}

func (s *Store) deletePost(id uint, ownerID uint) error {
	_, err := s.db.Exec(`
		DELETE FROM posts WHERE id = ? AND owner_id = ?
	`, id, ownerID)
	return err
}

func (s *Store) deposit(userID uint, amount int64) (User, error) {
	_, err := s.db.Exec(`
		UPDATE users SET balance = balance + ? WHERE id = ?
	`, amount, userID)
	if err != nil {
		return User{}, err
	}

	row := s.db.QueryRow(`
		SELECT id, username, name, email, phone, password, balance, is_admin
		FROM users WHERE id = ?
	`, userID)

	var user User
	var isAdmin int64
	if err := row.Scan(&user.ID, &user.Username, &user.Name, &user.Email, &user.Phone, &user.Password, &user.Balance, &isAdmin); err != nil {
		return User{}, err
	}
	return user, nil
}

func (s *Store) withdraw(userID uint, amount int64) (User, error) {
	var balance int64
	err := s.db.QueryRow(`SELECT balance FROM users WHERE id = ?`, userID).Scan(&balance)
	if err != nil {
		return User{}, err
	}
	if balance < amount {
		return User{}, errors.New("unbalance")
	}

	_, err = s.db.Exec(`
		UPDATE users SET balance = balance - ? WHERE id = ?
	`, amount, userID)
	if err != nil {
		return User{}, err
	}

	row := s.db.QueryRow(`
		SELECT id, username, name, email, phone, password, balance, is_admin
		FROM users WHERE id = ?
	`, userID)

	var user User
	var isAdmin int64
	if err := row.Scan(&user.ID, &user.Username, &user.Name, &user.Email, &user.Phone, &user.Password, &user.Balance, &isAdmin); err != nil {
		return User{}, err
	}
	return user, nil
}

func (s *Store) transfer(fromID uint, toUsername string, amount int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var balance int64
	err = tx.QueryRow(`SELECT balance FROM users WHERE id = ?`, fromID).Scan(&balance)
	if err != nil {
		return err
	}
	if balance < amount {
		return errors.New("unbalance")
	}

	var toID uint
	err = tx.QueryRow(`SELECT id FROM users WHERE username = ?`, strings.TrimSpace(toUsername)).Scan(&toID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("user not found")
		}
		return err
	}

	_, err = tx.Exec(`UPDATE users SET balance = balance - ? WHERE id = ?`, amount, fromID)
	if err != nil {
		return err
	}

	_, err = tx.Exec(`UPDATE users SET balance = balance + ? WHERE id = ?`, amount, toID)
	if err != nil {
		return err
	}

	return tx.Commit()
}
