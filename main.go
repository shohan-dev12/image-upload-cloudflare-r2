package main

import (
	"context"
	"encoding/json"
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
	Status  int     `json:"status"`
	URL     *string `json:"url"`
	Message string  `json:"message"`
}

var s3Client *s3.Client
var bucketName string
var publicURL string

func main() {
	_ = godotenv.Load()

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	initR2()

	http.HandleFunc("/upload", uploadHandler)

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
		config.WithEndpointResolver(
			aws.EndpointResolverFunc(func(service, region string) (aws.Endpoint, error) {
				return aws.Endpoint{
					URL: "https://" + accountID + ".r2.cloudflarestorage.com",
				}, nil
			}),
		),
	)

	if err != nil {
		log.Fatal("Failed to load R2 config:", err)
	}

	s3Client = s3.NewFromConfig(cfg)
}

func uploadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendJSON(w, 405, nil, "Method not allowed")
		return
	}

	err := r.ParseMultipartForm(10 << 20) // 10MB max
	if err != nil {
		sendJSON(w, 400, nil, "Invalid multipart form")
		return
	}

	file, header, err := r.FormFile("image")
	if err != nil {
		sendJSON(w, 400, nil, "Image file is required")
		return
	}
	defer file.Close()

	if !isAllowedImage(header) {
		sendJSON(w, 400, nil, "Invalid image type. Only jpeg, png, webp allowed")
		return
	}

	filename := generateFileName(header.Filename)

	url, err := uploadToR2(file, filename)
	if err != nil {
		sendJSON(w, 500, nil, "Failed to upload image")
		return
	}

	sendJSON(w, 200, &url, "Image uploaded successfully")
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

func sendJSON(w http.ResponseWriter, status int, url *string, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	json.NewEncoder(w).Encode(ApiResponse{
		Status:  status,
		URL:     url,
		Message: message,
	})
}