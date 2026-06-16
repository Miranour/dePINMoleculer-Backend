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
	jobRepo      *repository.JobRepository
	jobPublisher *service.JobPublisher
	redisClient  *redis.Client
	authService  *service.AuthService
}

func NewRESTServer(walletRepo *repository.WalletRepository, jobRepo *repository.JobRepository, jobPublisher *service.JobPublisher, redisClient *redis.Client, authService *service.AuthService) *RESTServer {
	r := gin.Default()
	
	// Add CORS middleware
	r.Use(CORSMiddleware())
	
	server := &RESTServer{
		router:       r,
		walletRepo:   walletRepo,
		jobRepo:      jobRepo,
		jobPublisher: jobPublisher,
		redisClient:  redisClient,
		authService:  authService,
	}

	server.setupRoutes()
	return server
}

func (s *RESTServer) Run(addr string) error {
	return s.router.Run(addr)
}

func (s *RESTServer) setupRoutes() {
	s.router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	v1 := s.router.Group("/api/v1")

	// --- WEB AUTH ENDPOINTS ---
	webAuth := v1.Group("/auth")
	{
		webAuth.POST("/register", s.webRegister)
		webAuth.POST("/login", s.webLogin)
		webAuth.POST("/google", s.googleLogin)
		webAuth.POST("/orcid/callback", s.orcidCallback)
	}

	// --- PROFILE ENDPOINTS ---
	profile := v1.Group("/profile")
	profile.Use(AuthMiddleware("researcher"))
	{
		profile.GET("", s.getProfile)
		profile.GET("/wallet", s.getUserWallet)
		profile.GET("/jobs", s.getUserJobs)
		profile.POST("/wallet/deposit", s.depositUserWallet)
	}

	// --- VERIFICATION ENDPOINTS ---
	verification := v1.Group("/verification")
	// Use a generic middleware or assume researcher for now
	verification.Use(AuthMiddleware("researcher"))
	{
		verification.POST("/start", s.startVerification)
		verification.GET("/status", s.verificationStatus)
		verification.POST("/verify", s.completeVerification)
	}

	// --- RESEARCHER ENDPOINTS ---
	researcher := v1.Group("/jobs")
	researcher.Use(AuthMiddleware("researcher"))
	researcher.Use(VerifiedMiddleware())
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
	// err := s.walletRepo.BlockUserBalance(c.Request.Context(), userID, req.Cost)
	// if err != nil {
	// 	c.JSON(http.StatusPaymentRequired, gin.H{"error": "Insufficient balance or user not found"})
	// 	return
	// }
	var err error

	// Save job to database
	// Assuming PDB ID can be derived or left empty for now. Using a generic ID or empty string.
	// For MVP, we pass empty string as PDB ID if not explicitly provided
	err = s.jobRepo.CreateJob(c.Request.Context(), jobID, userID, req.SmilesString, req.TargetPdbUrl, "", req.MaxExhaustiveness, req.Cost)
	if err != nil {
		// Rollback balance if DB fails (In a real app, this should be in a transaction, but for MVP we will just return error)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save job to database"})
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
func (s *RESTServer) webRegister(c *gin.Context) {
	type registerReq struct {
		Email    string `json:"email" binding:"required,email"`
		Password string `json:"password" binding:"required,min=6"`
	}
	var req registerReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	user, err := s.authService.RegisterEmailUser(c.Request.Context(), req.Email, req.Password)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	token, _ := GenerateToken(user.ID, user.Role, user.IsVerified)
	c.JSON(http.StatusCreated, gin.H{
		"token": token,
		"user": gin.H{
			"id":          user.ID,
			"email":       user.Email,
			"role":        user.Role,
			"is_verified": user.IsVerified,
		},
	})
}

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

	user, err := s.authService.LoginEmailUser(c.Request.Context(), req.Email, req.Password)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	token, _ := GenerateToken(user.ID, user.Role, user.IsVerified)
	c.JSON(http.StatusOK, gin.H{
		"token": token,
		"user": gin.H{
			"id":          user.ID,
			"email":       user.Email,
			"role":        user.Role,
			"is_verified": user.IsVerified,
		},
	})
}

func (s *RESTServer) googleLogin(c *gin.Context) {
	type googleReq struct {
		IDToken string `json:"id_token" binding:"required"`
	}
	var req googleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	user, err := s.authService.AuthenticateWithGoogle(c.Request.Context(), req.IDToken)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	token, _ := GenerateToken(user.ID, user.Role, user.IsVerified)
	c.JSON(http.StatusOK, gin.H{
		"token": token,
		"user": gin.H{
			"id":          user.ID,
			"email":       user.Email,
			"role":        user.Role,
			"is_verified": user.IsVerified,
		},
	})
}

func (s *RESTServer) orcidCallback(c *gin.Context) {
	// MVP: Mock ORCID callback implementation
	// Real implementation would exchange code for token with ORCID API
	c.JSON(http.StatusNotImplemented, gin.H{"error": "ORCID integration not fully implemented yet"})
}

// --- VERIFICATION HANDLERS ---
func (s *RESTServer) startVerification(c *gin.Context) {
	userID := c.GetString("userID")
	err := s.authService.StartVerification(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "Verification started"})
}

func (s *RESTServer) verificationStatus(c *gin.Context) {
	userID := c.GetString("userID")
	// fetch user to check status
	user, err := s.authService.GetUserDetails(c.Request.Context(), userID)
	if err != nil || user == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"is_verified": user.IsVerified})
}

func (s *RESTServer) completeVerification(c *gin.Context) {
	userID := c.GetString("userID")
	type verifyReq struct {
		Code string `json:"code" binding:"required"`
	}
	var req verifyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	err := s.authService.CompleteVerification(c.Request.Context(), userID, req.Code)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "Verified"})
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

	token, _ := GenerateToken(req.WorkerID, "worker", true)
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
// --- PROFILE HANDLERS ---
func (s *RESTServer) getProfile(c *gin.Context) {
	userID := c.GetString("userID")
	
	user, err := s.authService.GetUserDetails(c.Request.Context(), userID)
	if err != nil || user == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	// For MVP, we can just return 0 for stats, or query them if needed. 
	// To keep it simple, we query total jobs from jobRepo.
	_, totalJobs, _ := s.jobRepo.GetJobsByUserID(c.Request.Context(), userID, 1, 0)

	c.JSON(http.StatusOK, gin.H{
		"user": gin.H{
			"id":            user.ID,
			"email":         user.Email,
			"auth_provider": user.AuthProvider,
			"is_verified":   user.IsVerified,
			"role":          user.Role,
		},
		"stats": gin.H{
			"total_jobs":     totalJobs,
			"completed_jobs": 0, // Mock for now, could be added later
			"total_spent":    0, // Mock for now
		},
	})
}

func (s *RESTServer) getUserWallet(c *gin.Context) {
	userID := c.GetString("userID")
	wallet, err := s.walletRepo.GetUserWallet(c.Request.Context(), userID)
	if err != nil || wallet == nil {
		// Fallback for users created before automatic wallet generation
		_ = s.walletRepo.EnsureUserWallet(c.Request.Context(), userID)
		wallet, err = s.walletRepo.GetUserWallet(c.Request.Context(), userID)
		if err != nil || wallet == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Wallet not found"})
			return
		}
	}
	c.JSON(http.StatusOK, wallet)
}
func (s *RESTServer) getUserJobs(c *gin.Context) {
	userID := c.GetString("userID")
	
	// MVP hardcoded pagination values or we could parse from query
	limit := 10
	offset := 0
	
	jobs, total, err := s.jobRepo.GetJobsByUserID(c.Request.Context(), userID, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch jobs"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"jobs":  jobs,
		"total": total,
		"page":  1,
		"limit": limit,
	})
}

// --- WALLET DEPOSIT HANDLER ---
func (s *RESTServer) depositUserWallet(c *gin.Context) {
	userID := c.GetString("userID")

	type depositReq struct {
		Amount          float64 `json:"amount" binding:"required,gt=0"`
		PaymentMethodID string  `json:"payment_method_id" binding:"required"`
	}
	var req depositReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// MVP: Direct balance update. In production, this would:
	// 1. Create a Stripe PaymentIntent with req.PaymentMethodID
	// 2. Confirm the payment
	// 3. Only update balance after webhook confirmation
	// For now, we trust the frontend payment and update balance directly.

	err := s.walletRepo.DepositUserBalance(c.Request.Context(), userID, req.Amount)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to deposit: " + err.Error()})
		return
	}

	// Fetch updated wallet
	wallet, _ := s.walletRepo.GetUserWallet(c.Request.Context(), userID)

	c.JSON(http.StatusOK, gin.H{
		"status":      "SUCCESS",
		"new_balance": wallet.ActiveBalance,
	})
}
