package api

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"depin-backend/internal/repository"
	"depin-backend/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
)

type RESTServer struct {
	router       *gin.Engine
	walletRepo   *repository.WalletRepository
	jobPublisher *service.JobPublisher
	redisClient  *redis.Client
}

func NewRESTServer(walletRepo *repository.WalletRepository, jobPublisher *service.JobPublisher, redisClient *redis.Client) *RESTServer {
	r := gin.Default()
	
	server := &RESTServer{
		router:       r,
		walletRepo:   walletRepo,
		jobPublisher: jobPublisher,
		redisClient:  redisClient,
	}

	server.setupRoutes()
	return server
}

func (s *RESTServer) Run(addr string) error {
	return s.router.Run(addr)
}

func (s *RESTServer) setupRoutes() {
	v1 := s.router.Group("/api/v1")

	// --- WEB AUTH ENDPOINTS ---
	webAuth := v1.Group("/auth")
	{
		webAuth.POST("/login", s.webLogin)
	}

	// --- RESEARCHER ENDPOINTS ---
	researcher := v1.Group("/jobs")
	researcher.Use(AuthMiddleware("researcher"))
	{
		researcher.POST("", s.createJob)
		researcher.POST("/:id/abort", s.abortJob)
	}

	// --- WORKER ENDPOINTS ---
	workers := v1.Group("/workers")
	{
		workers.POST("/register", s.registerWorker)
		workers.POST("/auth", s.authWorker)

		authWorkers := workers.Group("")
		authWorkers.Use(AuthMiddleware("worker"))
		{
			authWorkers.GET("/:id/wallet", s.getWorkerWallet)
			authWorkers.GET("/:id/earnings", s.getWorkerEarnings)
			authWorkers.POST("/:id/withdraw", s.requestWithdrawal)
		}
	}

	// --- ADMIN ENDPOINTS ---
	admin := v1.Group("/admin")
	admin.Use(AuthMiddleware("admin"))
	{
		admin.GET("/withdrawals", s.listWithdrawals)
		admin.POST("/withdrawals/:id/approve", s.approveWithdrawal)
		admin.POST("/withdrawals/:id/reject", s.rejectWithdrawal)
	}

	// --- WEBSOCKET ---
	v1.GET("/ws/jobs/:id/progress", s.wsJobProgress)
}

// --- RESEARCHER HANDLERS ---
type createJobRequest struct {
	SmilesString      string  `json:"smiles_string" binding:"required"`
	TargetPdbUrl      string  `json:"target_pdb_url" binding:"required"`
	MaxExhaustiveness int32   `json:"max_exhaustiveness" binding:"required"`
	Cost              float64 `json:"cost" binding:"required"`
}

func (s *RESTServer) createJob(c *gin.Context) {
	var req createJobRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	userID := c.GetString("userID")
	jobID := uuid.New().String()

	// Block user balance
	err := s.walletRepo.BlockUserBalance(c.Request.Context(), userID, req.Cost)
	if err != nil {
		c.JSON(http.StatusPaymentRequired, gin.H{"error": "Insufficient balance or user not found"})
		return
	}

	// Publish job
	err = s.jobPublisher.PublishJob(c.Request.Context(), jobID, req.SmilesString, req.TargetPdbUrl, req.MaxExhaustiveness)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to queue job"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"job_id": jobID, "status": "QUEUED"})
}

// --- WEB AUTH HANDLERS ---
func (s *RESTServer) webLogin(c *gin.Context) {
	type loginReq struct {
		Email    string `json:"email" binding:"required"`
		Password string `json:"password" binding:"required"`
	}
	var req loginReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var role string
	var userID string
	var name string

	if req.Email == "admin@depin.com" && req.Password == "admin123" {
		role = "admin"
		userID = "1"
		name = "Admin User"
	} else if req.Email == "user@depin.com" && req.Password == "user123" {
		role = "researcher"
		userID = "2"
		name = "Researcher User"
	} else {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Geçersiz e-posta veya şifre"})
		return
	}

	token, _ := GenerateToken(userID, role)
	c.JSON(http.StatusOK, gin.H{
		"token": token,
		"user": gin.H{
			"id":    userID,
			"email": req.Email,
			"role":  role,
			"name":  name,
		},
	})
}

func (s *RESTServer) abortJob(c *gin.Context) {
	jobID := c.Param("id")
	// For now, emit a pubsub or queue message to abort. 
	// MVP: just return success
	log.Printf("Abort requested for job %s", jobID)
	c.JSON(http.StatusOK, gin.H{"status": "ABORT_SIGNAL_SENT"})
}

// --- WORKER HANDLERS ---
func (s *RESTServer) registerWorker(c *gin.Context) {
	// MVP: auto generate ID and return
	workerID := uuid.New().String()
	apiKey := uuid.New().String() // In a real app, save to DB
	c.JSON(http.StatusCreated, gin.H{"worker_id": workerID, "api_key": apiKey})
}

func (s *RESTServer) authWorker(c *gin.Context) {
	type authReq struct {
		WorkerID string `json:"worker_id" binding:"required"`
		APIKey   string `json:"api_key" binding:"required"`
	}
	var req authReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	token, _ := GenerateToken(req.WorkerID, "worker")
	c.JSON(http.StatusOK, gin.H{"token": token})
}

func (s *RESTServer) getWorkerWallet(c *gin.Context) {
	workerID := c.Param("id")
	if c.GetString("userID") != workerID {
		c.JSON(http.StatusForbidden, gin.H{"error": "Unauthorized"})
		return
	}

	wallet, err := s.walletRepo.GetWorkerWallet(c.Request.Context(), workerID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Wallet not found"})
		return
	}
	c.JSON(http.StatusOK, wallet)
}

func (s *RESTServer) getWorkerEarnings(c *gin.Context) {
	workerID := c.Param("id")
	earnings, err := s.walletRepo.GetWorkerEarnings(c.Request.Context(), workerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, earnings)
}

func (s *RESTServer) requestWithdrawal(c *gin.Context) {
	workerID := c.Param("id")
	type wdReq struct {
		Amount      float64 `json:"amount" binding:"required"`
		Currency    string  `json:"currency" binding:"required"`
		Method      string  `json:"payout_method" binding:"required"`
		Destination string  `json:"destination" binding:"required"`
	}
	var req wdReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	err := s.walletRepo.CreateWithdrawalRequest(c.Request.Context(), workerID, req.Amount, req.Currency, req.Method, req.Destination)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"status": "PENDING"})
}

// --- ADMIN HANDLERS ---
func (s *RESTServer) listWithdrawals(c *gin.Context) {
	withdrawals, err := s.walletRepo.GetPendingWithdrawals(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, withdrawals)
}

func (s *RESTServer) approveWithdrawal(c *gin.Context) {
	id := c.Param("id")
	if err := s.walletRepo.UpdateWithdrawalStatus(c.Request.Context(), id, "APPROVED"); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "APPROVED"})
}

func (s *RESTServer) rejectWithdrawal(c *gin.Context) {
	id := c.Param("id")
	if err := s.walletRepo.UpdateWithdrawalStatus(c.Request.Context(), id, "REJECTED"); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "REJECTED"})
}

// --- WEBSOCKET HANDLER ---
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func (s *RESTServer) wsJobProgress(c *gin.Context) {
	jobID := c.Param("id")
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("WS Upgrade error: %v", err)
		return
	}
	defer conn.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	channelName := fmt.Sprintf("room:job_%s", jobID)
	pubsub := s.redisClient.Subscribe(ctx, channelName)
	defer pubsub.Close()

	ch := pubsub.Channel()
	for msg := range ch {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(msg.Payload)); err != nil {
			log.Printf("WS Write error: %v", err)
			break
		}
	}
}
