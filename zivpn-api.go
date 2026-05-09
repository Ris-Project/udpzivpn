package main

import (
    "encoding/json"
    "flag"
    "fmt"
    "io/ioutil"
    "log"
    "net/http"
    "os"
    "os/exec"
    "strings"
    "sync"
    "time"
)

const (
    ConfigFile = "/etc/zivpn/config.json"
    UserDB     = "/etc/zivpn/users.json"
    DomainFile = "/etc/zivpn/domain"
    ApiKeyFile = "/etc/zivpn/apikey"
    Port       = "/etc/zivpn/api_port"
)

var AuthToken = "AutoFtBot-agskjgdvsbdreiWG1234512SDKrqw"

type Config struct {
    Listen string `json:"listen"`
    Cert   string `json:"cert"`
    Key    string `json:"key"`
    Obfs   string `json:"obfs"`
    Auth   struct {
        Mode   string   `json:"mode"`
        Config []string `json:"config"`
    } `json:"auth"`
}

type UserRequest struct {
    Password string `json:"password"`
    Days     int    `json:"days"`
    Expire   int64  `json:"expire"` // TAMBAHKAN INI
}

type UserStore struct {
    Password string `json:"password"`
    Expired  string `json:"expired"`
    Status   string `json:"status"`
}

type Response struct {
    Success bool        `json:"success"`
    Message string      `json:"message"`
    Data    interface{} `json:"data,omitempty"`
}

var mutex = &sync.Mutex{}

func main() {
    port := flag.Int("port", 8080, "Port to run the API server on")
    flag.Parse()

    if keyBytes, err := ioutil.ReadFile(ApiKeyFile); err == nil {
        AuthToken = strings.TrimSpace(string(keyBytes))
    }

    http.HandleFunc("/api/user/create", authMiddleware(createUser))
    http.HandleFunc("/api/user/delete", authMiddleware(deleteUser))
    http.HandleFunc("/api/user/renew", authMiddleware(renewUser))
    http.HandleFunc("/api/users", authMiddleware(listUsers))
    http.HandleFunc("/api/info", authMiddleware(getSystemInfo))
    http.HandleFunc("/api/cron/expire", authMiddleware(checkExpiration))
    http.HandleFunc("/api/cron/cleanup", authMiddleware(cleanupExpired))

    log.Printf("Server started at :%d", *port)
    log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", *port), nil))
}

func authMiddleware(next http.HandlerFunc) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        token := r.Header.Get("X-API-Key")

        if token != AuthToken {
            jsonResponse(w, http.StatusUnauthorized, false, "Unauthorized", nil)
            return
        }

        next(w, r)
    }
}

func jsonResponse(w http.ResponseWriter, status int, success bool, message string, data interface{}) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)

    json.NewEncoder(w).Encode(Response{
        Success: success,
        Message: message,
        Data:    data,
    })
}

// FUNGSI CREATE YANG SUDAH DIPERBAIKI
func createUser(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        jsonResponse(w, http.StatusMethodNotAllowed, false, "Method not allowed", nil)
        return
    }

    var req UserRequest

    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        jsonResponse(w, http.StatusBadRequest, false, "Invalid request body", nil)
        return
    }

    if req.Password == "" {
        jsonResponse(w, http.StatusBadRequest, false, "Password wajib diisi", nil)
        return
    }

    mutex.Lock()
    defer mutex.Unlock()

    config, err := loadConfig()
    if err != nil {
        jsonResponse(w, http.StatusInternalServerError, false, "Gagal membaca config", nil)
        return
    }

    for _, p := range config.Auth.Config {
        if p == req.Password {
            jsonResponse(w, http.StatusConflict, false, "User sudah ada", nil)
            return
        }
    }

    config.Auth.Config = append(config.Auth.Config, req.Password)

    if err := saveConfig(config); err != nil {
        jsonResponse(w, http.StatusInternalServerError, false, "Gagal menyimpan config", nil)
        return
    }

    // LOGIKA PERBAIKAN: Handle Trial (0) vs Hari (>0)
    var expDate string
    if req.Days == 0 {
        // Jika bot mengirim 0 (Trial), maka durasi 30 Menit
        expDate = time.Now().Add(30 * time.Minute).Format("2006-01-02 15:04:05")
    } else {
        // Jika normal, hitung berdasarkan hari
        expDate = time.Now().Add(time.Duration(req.Days) * 24 * time.Hour).Format("2006-01-02 15:04:05")
    }

    users, err := loadUsers()
    if err != nil {
        jsonResponse(w, http.StatusInternalServerError, false, "Gagal membaca database user", nil)
        return
    }

    newUser := UserStore{
        Password: req.Password,
        Expired:  expDate,
        Status:   "active",
    }

    users = append(users, newUser)

    if err := saveUsers(users); err != nil {
        jsonResponse(w, http.StatusInternalServerError, false, "Gagal menyimpan database user", nil)
        return
    }

    if err := restartService(); err != nil {
        jsonResponse(w, http.StatusInternalServerError, false, "Gagal restart service", nil)
        return
    }

    domain := "Tidak diatur"

    if domainBytes, err := ioutil.ReadFile(DomainFile); err == nil {
        domain = strings.TrimSpace(string(domainBytes))
    }

    jsonResponse(w, http.StatusOK, true, "User berhasil dibuat", map[string]string{
        "password": req.Password,
        "expired":  expDate,
        "domain":   domain,
    })
}

func deleteUser(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        jsonResponse(w, http.StatusMethodNotAllowed, false, "Method not allowed", nil)
        return
    }

    var req UserRequest

    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        jsonResponse(w, http.StatusBadRequest, false, "Invalid request body", nil)
        return
    }

    mutex.Lock()
    defer mutex.Unlock()

    config, err := loadConfig()
    if err != nil {
        jsonResponse(w, http.StatusInternalServerError, false, "Gagal membaca config", nil)
        return
    }

    foundInConfig := false
    newConfigAuth := []string{}

    for _, p := range config.Auth.Config {
        if p == req.Password {
            foundInConfig = true
        } else {
            newConfigAuth = append(newConfigAuth, p)
        }
    }

    if foundInConfig {
        config.Auth.Config = newConfigAuth

        if err := saveConfig(config); err != nil {
            jsonResponse(w, http.StatusInternalServerError, false, "Gagal menyimpan config", nil)
            return
        }
    }

    users, err := loadUsers()
    if err != nil {
        jsonResponse(w, http.StatusInternalServerError, false, "Gagal membaca database user", nil)
        return
    }

    foundInDB := false
    newUsers := []UserStore{}

    for _, u := range users {
        if u.Password == req.Password {
            foundInDB = true
            continue
        }

        newUsers = append(newUsers, u)
    }

    if foundInDB {
        if err := saveUsers(newUsers); err != nil {
            jsonResponse(w, http.StatusInternalServerError, false, "Gagal menyimpan users", nil)
            return
        }
    }

    if foundInConfig {
        restartService()
    }

    jsonResponse(w, http.StatusOK, true, "User berhasil dihapus", nil)
}

func renewUser(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        jsonResponse(w, http.StatusMethodNotAllowed, false, "Method not allowed", nil)
        return
    }

    var req UserRequest

    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        jsonResponse(w, http.StatusBadRequest, false, "Invalid request body", nil)
        return
    }

    mutex.Lock()
    defer mutex.Unlock()

    users, err := loadUsers()
    if err != nil {
        jsonResponse(w, http.StatusInternalServerError, false, "Gagal membaca users", nil)
        return
    }

    found := false
    newUsers := []UserStore{}
    var newExpDate string

    for _, u := range users {
        if u.Password == req.Password {
            found = true

            currentExp, err := time.Parse("2006-01-02 15:04:05", u.Expired)

            if err != nil {
                currentExp = time.Now()
            }

            if currentExp.Before(time.Now()) {
                currentExp = time.Now()
            }

            newExp := currentExp.Add(time.Duration(req.Days) * 24 * time.Hour)

            newExpDate = newExp.Format("2006-01-02 15:04:05")

            u.Expired = newExpDate

            if u.Status == "locked" {
                u.Status = "active"
                go enableUser(req.Password)
            }

            newUsers = append(newUsers, u)

        } else {
            newUsers = append(newUsers, u)
        }
    }

    if !found {
        jsonResponse(w, http.StatusNotFound, false, "User tidak ditemukan", nil)
        return
    }

    if err := saveUsers(newUsers); err != nil {
        jsonResponse(w, http.StatusInternalServerError, false, "Gagal menyimpan users", nil)
        return
    }

    restartService()

    jsonResponse(w, http.StatusOK, true, "User berhasil diperpanjang", map[string]string{
        "password": req.Password,
        "expired":  newExpDate,
    })
}

func listUsers(w http.ResponseWriter, r *http.Request) {
    users, err := loadUsers()

    if err != nil {
        jsonResponse(w, http.StatusInternalServerError, false, "Gagal membaca users", nil)
        return
    }

    type UserInfo struct {
        Password string `json:"password"`
        Expired  string `json:"expired"`
        Status   string `json:"status"`
    }

    userList := []UserInfo{}

    for _, u := range users {
        status := "Active"

        if u.Status == "locked" {
            status = "Locked"
        } else {
            expTime, err := time.Parse("2006-01-02 15:04:05", u.Expired)

            if err == nil && time.Now().After(expTime) {
                status = "Expired"
            }
        }

        userList = append(userList, UserInfo{
            Password: u.Password,
            Expired:  u.Expired,
            Status:   status,
        })
    }

    jsonResponse(w, http.StatusOK, true, "Daftar user", userList)
}

func getSystemInfo(w http.ResponseWriter, r *http.Request) {
    cmd := exec.Command("curl", "-s", "ifconfig.me")
    ipPub, _ := cmd.Output()

    cmd = exec.Command("hostname", "-I")
    ipPriv, _ := cmd.Output()

    domain := "Tidak diatur"

    if domainBytes, err := ioutil.ReadFile(DomainFile); err == nil {
        domain = strings.TrimSpace(string(domainBytes))
    }

    info := map[string]string{
        "domain":     domain,
        "public_ip":  strings.TrimSpace(string(ipPub)),
        "private_ip": strings.Fields(string(ipPriv))[0],
        "port":       "5667",
        "service":    "zivpn",
    }

    jsonResponse(w, http.StatusOK, true, "System Info", info)
}

func checkExpiration(w http.ResponseWriter, r *http.Request) {
    users, err := loadUsers()

    if err != nil {
        jsonResponse(w, http.StatusInternalServerError, false, "Gagal membaca users", nil)
        return
    }

    config, err := loadConfig()

    if err != nil {
        jsonResponse(w, http.StatusInternalServerError, false, "Gagal membaca config", nil)
        return
    }

    activeUsers := make(map[string]bool)

    for _, p := range config.Auth.Config {
        activeUsers[p] = true
    }

    revokedCount := 0

    for _, u := range users {
        expTime, err := time.Parse("2006-01-02 15:04:05", u.Expired)

        if err == nil && time.Now().After(expTime) && activeUsers[u.Password] {
            log.Printf("User %s expired. Revoking access.\n", u.Password)

            revokeAccess(u.Password)

            revokedCount++
        }
    }

    jsonResponse(w, http.StatusOK, true,
        fmt.Sprintf("Expiration check complete. Revoked: %d", revokedCount), nil)
}

func cleanupExpired(w http.ResponseWriter, r *http.Request) {
    mutex.Lock()
    defer mutex.Unlock()

    users, err := loadUsers()

    if err != nil {
        jsonResponse(w, http.StatusInternalServerError, false, "Gagal membaca users", nil)
        return
    }

    config, err := loadConfig()

    if err != nil {
        jsonResponse(w, http.StatusInternalServerError, false, "Gagal membaca config", nil)
        return
    }

    expiredPasswords := make(map[string]bool)

    for _, u := range users {
        expTime, err := time.Parse("2006-01-02 15:04:05", u.Expired)

        if err == nil && time.Now().After(expTime) {
            expiredPasswords[u.Password] = true
        }
    }

    if len(expiredPasswords) == 0 {
        // KEMBALIKAN DATA KOSONG SESUAI FORMAT YANG DIBUTUHKAN BOT
        jsonResponse(w, http.StatusOK, true, "Tidak ada akun expired", map[string]interface{}{
            "deleted_count": 0,
            "deleted_users": []string{},
        })
        return
    }

    activeUsers := []UserStore{}

    for _, u := range users {
        if !expiredPasswords[u.Password] {
            activeUsers = append(activeUsers, u)
        }
    }

    activeConfig := []string{}

    for _, p := range config.Auth.Config {
        if !expiredPasswords[p] {
            activeConfig = append(activeConfig, p)
        }
    }

    config.Auth.Config = activeConfig

    saveUsers(activeUsers)
    saveConfig(config)

    restartService()

    // SIAPKAN LIST USER YANG DIHAPUS UNTUK LAPORAN
    deletedList := make([]string, 0, len(expiredPasswords))
    for pwd := range expiredPasswords {
        deletedList = append(deletedList, pwd)
    }

    // KEMBALIKAN DATA JSON YANG BENAR
    jsonResponse(w, http.StatusOK, true,
        fmt.Sprintf("Berhasil menghapus %d akun expired", len(expiredPasswords)), 
        map[string]interface{}{
            "deleted_count": len(expiredPasswords),
            "deleted_users": deletedList,
        },
    )
}
func revokeAccess(password string) {
    mutex.Lock()
    defer mutex.Unlock()

    config, err := loadConfig()

    if err != nil {
        return
    }

    newConfigAuth := []string{}
    changed := false

    for _, p := range config.Auth.Config {
        if p == password {
            changed = true
        } else {
            newConfigAuth = append(newConfigAuth, p)
        }
    }

    if changed {
        config.Auth.Config = newConfigAuth
        saveConfig(config)
        restartService()
    }
}

func enableUser(password string) {
    mutex.Lock()
    defer mutex.Unlock()

    config, err := loadConfig()

    if err != nil {
        return
    }

    exists := false

    for _, p := range config.Auth.Config {
        if p == password {
            exists = true
            break
        }
    }

    if !exists {
        config.Auth.Config = append(config.Auth.Config, password)
        saveConfig(config)
        restartService()
    }
}

func loadConfig() (Config, error) {
    var config Config

    file, err := ioutil.ReadFile(ConfigFile)

    if err != nil {
        return config, err
    }

    err = json.Unmarshal(file, &config)

    return config, err
}

func saveConfig(config Config) error {
    data, err := json.MarshalIndent(config, "", "  ")

    if err != nil {
        return err
    }

    return ioutil.WriteFile(ConfigFile, data, 0644)
}

func loadUsers() ([]UserStore, error) {
    var users []UserStore

    file, err := ioutil.ReadFile(UserDB)

    if err != nil {
        if os.IsNotExist(err) {
            return users, nil
        }

        return nil, err
    }

    err = json.Unmarshal(file, &users)

    return users, err
}

func saveUsers(users []UserStore) error {
    data, err := json.MarshalIndent(users, "", "  ")

    if err != nil {
        return err
    }

    return ioutil.WriteFile(UserDB, data, 0644)
}

func restartService() error {
    cmd := exec.Command("systemctl", "restart", "zivpn.service")
    return cmd.Run()
}