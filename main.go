package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
)

type ApiResponse struct {
	Status  int      `json:"status"`
	URLs    []string `json:"urls"`
	Message string   `json:"message"`
	Failed  []string `json:"failed,omitempty"`
}

type HealthResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

var s3Client *s3.Client
var bucketName string
var publicURL string
var apiKey string

func main() {
	_ = godotenv.Load()

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	initR2()

	apiKey = os.Getenv("API_KEY")
	if apiKey == "" {
		log.Fatal("Missing required environment variable: API_KEY")
	}

	http.HandleFunc("/", authMiddleware(healthHandler))
	http.HandleFunc("/upload", authMiddleware(uploadHandler))

	certFile := os.Getenv("TLS_CERT_FILE")
	keyFile := os.Getenv("TLS_KEY_FILE")

	if certFile != "" && keyFile != "" {
		log.Println("🚀 Server running on port", port, "(HTTPS)")
		log.Fatal(http.ListenAndServeTLS(":"+port, certFile, keyFile, nil))
	} else {
		log.Println("🚀 Server running on port", port, "(HTTP)")
		log.Fatal(http.ListenAndServe(":"+port, nil))
	}
}

func authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-API-Key")
		if key != apiKey {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(401)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":  401,
				"message": "Unauthorized: Invalid or missing API key",
			})
			return
		}
		next(w, r)
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(HealthResponse{
		Success: true,
		Message: "successfully connect",
	})
}

func initR2() {
	bucketName = os.Getenv("R2_BUCKET_NAME")
	publicURL = os.Getenv("R2_PUBLIC_URL")
	accessKey := os.Getenv("R2_ACCESS_KEY")
	secretKey := os.Getenv("R2_SECRET_KEY")
	accountID := os.Getenv("R2_ACCOUNT_ID")

	if bucketName == "" || publicURL == "" || accessKey == "" || secretKey == "" || accountID == "" {
		log.Fatal("Missing required environment variables: R2_BUCKET_NAME, R2_PUBLIC_URL, R2_ACCESS_KEY, R2_SECRET_KEY, R2_ACCOUNT_ID")
	}

	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion("auto"),
		config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		),
	)

	if err != nil {
		log.Fatal("Failed to load R2 config:", err)
	}

	s3Client = s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String("https://" + accountID + ".r2.cloudflarestorage.com")
	})
}

func uploadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendJSONMulti(w, 405, nil, nil, "Method not allowed")
		return
	}

	err := r.ParseMultipartForm(50 << 20) // 50MB max for 5 images
	if err != nil {
		sendJSONMulti(w, 400, nil, nil, "Invalid multipart form")
		return
	}

	files := r.MultipartForm.File["images"]
	if len(files) == 0 {
		sendJSONMulti(w, 400, nil, nil, "At least 1 image required")
		return
	}
	if len(files) > 5 {
		sendJSONMulti(w, 400, nil, nil, "Maximum 5 images allowed")
		return
	}

	var urls []string
	var failed []string

	for _, fileHeader := range files {
		if !isAllowedImage(fileHeader) {
			failed = append(failed, fileHeader.Filename+": Invalid type")
			continue
		}

		file, err := fileHeader.Open()
		if err != nil {
			failed = append(failed, fileHeader.Filename+": Failed to open")
			continue
		}

		filename := generateFileName(fileHeader.Filename)
		url, err := uploadToR2(file, filename)
		file.Close()

		if err != nil {
			failed = append(failed, fileHeader.Filename+": Upload failed")
			continue
		}

		urls = append(urls, url)
	}

	if len(urls) == 0 {
		sendJSONMulti(w, 400, nil, failed, "All uploads failed")
		return
	}

	if len(failed) > 0 {
		msg := fmt.Sprintf("%d of %d images uploaded", len(urls), len(files))
		sendJSONMulti(w, 207, urls, failed, msg)
		return
	}

	msg := fmt.Sprintf("%d image(s) uploaded successfully", len(urls))
	sendJSONMulti(w, 200, urls, nil, msg)
}

func isAllowedImage(header *multipart.FileHeader) bool {
	ext := strings.ToLower(filepath.Ext(header.Filename))
	allowed := map[string]bool{
		".jpg":  true,
		".jpeg": true,
		".png":  true,
		".webp": true,
	}
	return allowed[ext]
}

func generateFileName(original string) string {
	ext := filepath.Ext(original)
	return "uploads/" + uuid.New().String() + ext
}

func uploadToR2(file multipart.File, filename string) (string, error) {
	_, err := s3Client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket:      aws.String(bucketName),
		Key:         aws.String(filename),
		Body:        file,
		ContentType: aws.String(detectContentType(filename)),
	})
	if err != nil {
		return "", err
	}

	url := publicURL + "/" + filename
	return url, nil
}

func detectContentType(filename string) string {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	default:
		return "application/octet-stream"
	}
}

func sendJSONMulti(w http.ResponseWriter, status int, urls []string, failed []string, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	json.NewEncoder(w).Encode(ApiResponse{
		Status:  status,
		URLs:    urls,
		Message: message,
		Failed:  failed,
	})
}