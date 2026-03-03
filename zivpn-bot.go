package main

import (
    "archive/zip"
    "bytes"
    "encoding/json"
    "fmt"
    "io"
    "io/ioutil"
    "log"
    "math/rand"
    "net/http"
    "os"
    "os/exec"
    "path/filepath"
    "regexp"
    "strconv"
    "strings"
    "time"

    tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// ==========================================
// Constants & Configuration
// ==========================================

const (
    BotConfigFile = "/etc/zivpn/bot-config.json"
    ApiPortFile   = "/etc/zivpn/api_port"
    ApiKeyFile    = "/etc/zivpn/apikey"
    DomainFile    = "/etc/zivpn/domain"
    PortFile      = "/etc/zivpn/port"
)

var ApiUrl = "http://127.0.0.1:443/api" // Default placeholder

var ApiKey = "AutoFtBot-agskjgdvsbdreiWG1234512SDKrqw"

type BotConfig struct {
    BotToken string `json:"bot_token"`
    AdminID  int64  `json:"admin_id"`
    Mode     string `json:"mode"`   // "public" or "private"
    Domain   string `json:"domain"` // Domain from setup
}

type IpInfo struct {
    City  string `json:"city"`
    Isp   string `json:"isp"`
    Query string `json:"query"`
}

type UserData struct {
    Password string `json:"password"`
    Expired  string `json:"expired"`
    Status   string `json:"status"`
    IpLimit  int    `json:"ip_limit"`
}

// ==========================================
// Global State
// ==========================================

var userStates = make(map[int64]string)
var tempUserData = make(map[int64]map[string]string)
var lastMessageIDs = make(map[int64]int)

// ==========================================
// Main Entry Point
// ==========================================

func main() {
    // Initialize Random Seed for Trial Username
    rand.Seed(time.Now().UnixNano())

    // Load API Key
    if keyBytes, err := ioutil.ReadFile(ApiKeyFile); err == nil {
        ApiKey = strings.TrimSpace(string(keyBytes))
    }

    // Load API Port
    if portBytes, err := ioutil.ReadFile(ApiPortFile); err == nil {
        port := strings.TrimSpace(string(portBytes))
        ApiUrl = fmt.Sprintf("http://127.0.0.1:%s/api", port)
    }

    // Load Config
    config, err := loadConfig()
    if err != nil {
        log.Fatal("Gagal memuat konfigurasi bot:", err)
    }

    // Initialize Bot
    bot, err := tgbotapi.NewBotAPI(config.BotToken)
    if err != nil {
        log.Panic(err)
    }

    bot.Debug = false
    log.Printf("Authorized on account %s", bot.Self.UserName)

    u := tgbotapi.NewUpdate(0)
    u.Timeout = 60
    updates := bot.GetUpdatesChan(u)

    // Main Loop
    for update := range updates {
        if update.Message != nil {
            handleMessage(bot, update.Message, &config)
        } else if update.CallbackQuery != nil {
            handleCallback(bot, update.CallbackQuery, &config)
        }
    }
}

// ==========================================
// Telegram Event Handlers
// ==========================================

func handleMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message, config *BotConfig) {
    // Access Control
    if !isAllowed(config, msg.From.ID) {
        replyError(bot, msg.Chat.ID, "⛔ Akses Ditolak. Bot ini Private.")
        return
    }

    // Handle Document Upload (Restore)
    if msg.Document != nil && msg.From.ID == config.AdminID {
        if state, exists := userStates[msg.From.ID]; exists && state == "waiting_restore_file" {
            processRestoreFile(bot, msg, config)
            return
        }
    }

    // Handle State (User Input)
    if state, exists := userStates[msg.From.ID]; exists {
        handleState(bot, msg, state, config)
        return
    }

    // Handle Commands
    if msg.IsCommand() {
        switch msg.Command() {
        case "start":
            showMainMenu(bot, msg.Chat.ID, config)
        default:
            replyError(bot, msg.Chat.ID, "Perintah tidak dikenal.")
        }
    }
}

func handleCallback(bot *tgbotapi.BotAPI, query *tgbotapi.CallbackQuery, config *BotConfig) {
    // Access Control
    if !isAllowed(config, query.From.ID) {
        if query.Data != "toggle_mode" || query.From.ID != config.AdminID {
            bot.Request(tgbotapi.NewCallback(query.ID, "Akses Ditolak"))
            return
        }
    }

    chatID := query.Message.Chat.ID
    userID := query.From.ID

    switch {
    // --- Menu Navigation ---
    case query.Data == "menu_create":
        startCreateUser(bot, chatID, userID)
    case query.Data == "menu_trial":
        // Pass config to startTrialUser
        startTrialUser(bot, chatID, userID, config)
    case query.Data == "menu_delete":
        showUserSelection(bot, chatID, 1, "delete")
    case query.Data == "menu_renew":
        showUserSelection(bot, chatID, 1, "renew")
    case query.Data == "menu_list":
        if userID == config.AdminID {
            listUsers(bot, chatID)
        }
    case query.Data == "menu_cleanup":
        if userID == config.AdminID {
            cleanupExpiredUsers(bot, chatID, config)
        }
    case query.Data == "menu_info":
        if userID == config.AdminID {
            systemInfo(bot, chatID, config)
        }
    case query.Data == "menu_backup_restore":
        if userID == config.AdminID {
            showBackupRestoreMenu(bot, chatID)
        }
    case query.Data == "menu_backup_action":
        if userID == config.AdminID {
            performBackup(bot, chatID)
        }
    case query.Data == "menu_restore_action":
        if userID == config.AdminID {
            startRestore(bot, chatID, userID)
        }
    case query.Data == "cancel":
        cancelOperation(bot, chatID, userID, config)

    // --- Pagination ---
    case strings.HasPrefix(query.Data, "page_"):
        handlePagination(bot, chatID, query.Data)

    // --- Action Selection ---
    case strings.HasPrefix(query.Data, "select_renew:"):
        startRenewUser(bot, chatID, userID, query.Data)
    case strings.HasPrefix(query.Data, "select_delete:"):
        confirmDeleteUser(bot, chatID, query.Data)

    // --- Action Confirmation ---
    case strings.HasPrefix(query.Data, "confirm_delete:"):
        username := strings.TrimPrefix(query.Data, "confirm_delete:")
        deleteUser(bot, chatID, username, config)

    // --- Admin Actions ---
    case query.Data == "toggle_mode":
        toggleMode(bot, chatID, userID, config)
    }

    bot.Request(tgbotapi.NewCallback(query.ID, ""))
}

func handleState(bot *tgbotapi.BotAPI, msg *tgbotapi.Message, state string, config *BotConfig) {
    userID := msg.From.ID
    text := strings.TrimSpace(msg.Text)
    chatID := msg.Chat.ID

    switch state {
    case "create_username":
        if !validateUsername(bot, chatID, text) {
            return
        }
        tempUserData[userID]["username"] = text
        userStates[userID] = "create_days"
        sendMessage(bot, chatID, "⏳ Masukkan Durasi (hari):")

    case "create_days":
        days, ok := validateNumber(bot, chatID, text, 1, 9999, "Durasi")
        if !ok {
            return
        }
        // For standard creation, we set default limits: Login 2, Quota 0 (Unlimited)
        createUser(bot, chatID, tempUserData[userID]["username"], days, 2, 0, config)
        resetState(userID)

    case "renew_days":
        days, ok := validateNumber(bot, chatID, text, 1, 9999, "Durasi")
        if !ok {
            return
        }
        renewUser(bot, chatID, tempUserData[userID]["username"], days, config)
        resetState(userID)
    }
}

// ==========================================
// Feature Implementation
// ==========================================

func startCreateUser(bot *tgbotapi.BotAPI, chatID int64, userID int64) {
    userStates[userID] = "create_username"
    tempUserData[userID] = make(map[string]string)
    sendMessage(bot, chatID, "👤 Masukkan Username/Password:")
}

// Modified: Auto generate trial-XXXX
func startTrialUser(bot *tgbotapi.BotAPI, chatID int64, userID int64, config *BotConfig) {
    // Generate random 4 digit number
    randomNum := rand.Intn(10000) // 0 to 9999
    username := fmt.Sprintf("trial-%04d", randomNum)

    sendMessage(bot, chatID, fmt.Sprintf("🎁 Membuat akun *TRIAL*\n👤 User: `%s`", username))
    
    // Trial Config: 1 Day, Max Login 2, Quota 1000 GB
    createUser(bot, chatID, username, 1, 2, 1000, config)
}

func startRenewUser(bot *tgbotapi.BotAPI, chatID int64, userID int64, data string) {
    username := strings.TrimPrefix(data, "select_renew:")
    tempUserData[userID] = map[string]string{"username": username}
    userStates[userID] = "renew_days"
    sendMessage(bot, chatID, fmt.Sprintf("🔄 Renewing %s\n⏳ Masukkan Tambahan Durasi (hari):", username))
}

func confirmDeleteUser(bot *tgbotapi.BotAPI, chatID int64, data string) {
    username := strings.TrimPrefix(data, "select_delete:")
    msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("❓ Yakin ingin menghapus user `%s`?", username))
    msg.ParseMode = "Markdown"
    msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
        tgbotapi.NewInlineKeyboardRow(
            tgbotapi.NewInlineKeyboardButtonData("✅ Ya, Hapus", "confirm_delete:"+username),
            tgbotapi.NewInlineKeyboardButtonData("❌ Batal", "cancel"),
        ),
    )
    sendAndTrack(bot, msg)
}

func cancelOperation(bot *tgbotapi.BotAPI, chatID int64, userID int64, config *BotConfig) {
    resetState(userID)
    showMainMenu(bot, chatID, config)
}

func handlePagination(bot *tgbotapi.BotAPI, chatID int64, data string) {
    parts := strings.Split(data, ":")
    action := parts[0][5:] // remove "page_"
    page, _ := strconv.Atoi(parts[1])
    showUserSelection(bot, chatID, page, action)
}

func toggleMode(bot *tgbotapi.BotAPI, chatID int64, userID int64, config *BotConfig) {
    if userID != config.AdminID {
        return
    }
    if config.Mode == "public" {
        config.Mode = "private"
    } else {
        config.Mode = "public"
    }
    saveConfig(config)
    showMainMenu(bot, chatID, config)
}

// Updated to support ipLimit and quota
func createUser(bot *tgbotapi.BotAPI, chatID int64, username string, days int, ipLimit int, quotaGB int, config *BotConfig) {
    res, err := apiCall("POST", "/user/create", map[string]interface{}{
        "password": username,
        "days":     days,
        "ip_limit": ipLimit,
        "quota":    quotaGB,
    })

    if err != nil {
        replyError(bot, chatID, "Error API: "+err.Error())
        return
    }

    if res["success"] == true {
        data := res["data"].(map[string]interface{})
        sendAccountInfo(bot, chatID, data, config)
    } else {
        replyError(bot, chatID, fmt.Sprintf("Gagal: %s", res["message"]))
        showMainMenu(bot, chatID, config)
    }
}

func renewUser(bot *tgbotapi.BotAPI, chatID int64, username string, days int, config *BotConfig) {
    res, err := apiCall("POST", "/user/renew", map[string]interface{}{
        "password": username,
        "days":     days,
    })

    if err != nil {
        replyError(bot, chatID, "Error API: "+err.Error())
        return
    }

    if res["success"] == true {
        data := res["data"].(map[string]interface{})
        sendAccountInfo(bot, chatID, data, config)
    } else {
        replyError(bot, chatID, fmt.Sprintf("Gagal: %s", res["message"]))
        showMainMenu(bot, chatID, config)
    }
}

func deleteUser(bot *tgbotapi.BotAPI, chatID int64, username string, config *BotConfig) {
    res, err := apiCall("POST", "/user/delete", map[string]interface{}{
        "password": username,
    })

    if err != nil {
        replyError(bot, chatID, "Error API: "+err.Error())
        return
    }

    if res["success"] == true {
        msg := tgbotapi.NewMessage(chatID, "✅ Password berhasil dihapus.")
        deleteLastMessage(bot, chatID)
        bot.Send(msg)
        showMainMenu(bot, chatID, config)
    } else {
        replyError(bot, chatID, fmt.Sprintf("Gagal: %s", res["message"]))
        showMainMenu(bot, chatID, config)
    }
}

func listUsers(bot *tgbotapi.BotAPI, chatID int64) {
    res, err := apiCall("GET", "/users", nil)
    if err != nil {
        replyError(bot, chatID, "Error API: "+err.Error())
        return
    }

    if res["success"] == true {
        users := res["data"].([]interface{})
        if len(users) == 0 {
            sendMessage(bot, chatID, "📂 Tidak ada user.")
            return
        }

        msg := "📋 *List Passwords*\n"
        for _, u := range users {
            user := u.(map[string]interface{})
            status := "🟢"
            if user["status"] == "Expired" {
                status = "🔴"
            }
            msg += fmt.Sprintf("\n%s `%s` (%s)", status, user["password"], user["expired"])
        }

        reply := tgbotapi.NewMessage(chatID, msg)
        reply.ParseMode = "Markdown"
        sendAndTrack(bot, reply)
    } else {
        replyError(bot, chatID, "Gagal mengambil data.")
    }
}

func systemInfo(bot *tgbotapi.BotAPI, chatID int64, config *BotConfig) {
    res, err := apiCall("GET", "/info", nil)
    if err != nil {
        replyError(bot, chatID, "Error API: "+err.Error())
        return
    }

    if res["success"] == true {
        data := res["data"].(map[string]interface{})
        ipInfo, _ := getIpInfo()

        // Enhanced UI
        msg := fmt.Sprintf(
            "╔══════════════════════╗\n"+
                "║    ⚙️ SYSTEM INFO ⚙️    ║\n"+
                "╠══════════════════════╣\n"+
                "║ 🌐 Domain : `%s`\n"+
                "║ 🌍 IP Pub : `%s`\n"+
                "║ 🔌 Port   : `%s`\n"+
                "║ 🚀 Status : `%s`\n"+
                "╠══════════════════════╣\n"+
                "║ 📍 Location Details  ║\n"+
                "╠══════════════════════╣\n"+
                "║ 🏙️ City   : `%s`\n"+
                "║ 📡 ISP   : `%s`\n"+
                "╚══════════════════════╝",
            config.Domain, data["public_ip"], data["port"], data["service"], ipInfo.City, ipInfo.Isp)

        reply := tgbotapi.NewMessage(chatID, msg)
        deleteLastMessage(bot, chatID)
        bot.Send(reply)
        showMainMenu(bot, chatID, config)
    } else {
        replyError(bot, chatID, "Gagal mengambil info.")
    }
}

func cleanupExpiredUsers(bot *tgbotapi.BotAPI, chatID int64, config *BotConfig) {
    sendMessage(bot, chatID, "⏳ Sedang membersihkan akun expired...")

    res, err := apiCall("POST", "/cron/cleanup", nil)
    if err != nil {
        replyError(bot, chatID, "Error API: "+err.Error())
        showMainMenu(bot, chatID, config)
        return
    }

    if res["success"] == true {
        data := res["data"]
        if data != nil {
            dataMap := data.(map[string]interface{})
            count := int(dataMap["deleted_count"].(float64))
            if count > 0 {
                deletedUsers := dataMap["deleted_users"].([]interface{})
                userList := ""
                for _, u := range deletedUsers {
                    userList += fmt.Sprintf("\n• `%s`", u.(string))
                }
                msg := fmt.Sprintf("✅ Berhasil menghapus %d akun expired:%s", count, userList)
                reply := tgbotapi.NewMessage(chatID, msg)
                reply.ParseMode = "Markdown"
                deleteLastMessage(bot, chatID)
                bot.Send(reply)
            } else {
                sendMessage(bot, chatID, "✅ Tidak ada akun expired yang perlu dihapus.")
            }
        } else {
            sendMessage(bot, chatID, "✅ "+res["message"].(string))
        }
    } else {
        replyError(bot, chatID, fmt.Sprintf("Gagal: %s", res["message"]))
    }
    showMainMenu(bot, chatID, config)
}

func showBackupRestoreMenu(bot *tgbotapi.BotAPI, chatID int64) {
    msg := tgbotapi.NewMessage(chatID, "💾 *Backup & Restore*\nSilakan pilih menu:")
    msg.ParseMode = "Markdown"
    msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
        tgbotapi.NewInlineKeyboardRow(
            tgbotapi.NewInlineKeyboardButtonData("⬇️ Backup Data", "menu_backup_action"),
            tgbotapi.NewInlineKeyboardButtonData("⬆️ Restore Data", "menu_restore_action"),
        ),
        tgbotapi.NewInlineKeyboardRow(
            tgbotapi.NewInlineKeyboardButtonData("❌ Kembali", "cancel"),
        ),
    )
    sendAndTrack(bot, msg)
}

func performBackup(bot *tgbotapi.BotAPI, chatID int64) {
    sendMessage(bot, chatID, "⏳ Sedang membuat backup...")

    files := []string{
        "/etc/zivpn/config.json",
        "/etc/zivpn/users.json",
        "/etc/zivpn/domain",
    }

    buf := new(bytes.Buffer)
    zipWriter := zip.NewWriter(buf)

    for _, file := range files {
        if _, err := os.Stat(file); os.IsNotExist(err) {
            continue
        }

        f, err := os.Open(file)
        if err != nil {
            continue
        }
        defer f.Close()

        w, err := zipWriter.Create(filepath.Base(file))
        if err != nil {
            continue
        }

        if _, err := io.Copy(w, f); err != nil {
            continue
        }
    }

    zipWriter.Close()

    fileName := fmt.Sprintf("zivpn-backup-%s.zip", time.Now().Format("20060102-150405"))
    tmpFile := "/tmp/" + fileName
    if err := ioutil.WriteFile(tmpFile, buf.Bytes(), 0644); err != nil {
        replyError(bot, chatID, "Gagal membuat file backup.")
        return
    }
    defer os.Remove(tmpFile)

    doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(tmpFile))
    doc.Caption = "✅ Backup Data ZiVPN"

    deleteLastMessage(bot, chatID)
    bot.Send(doc)
}

func startRestore(bot *tgbotapi.BotAPI, chatID int64, userID int64) {
    userStates[userID] = "waiting_restore_file"
    sendMessage(bot, chatID, "⬆️ *Restore Data*\n\nSilakan kirim file ZIP backup Anda sekarang.\n\n⚠️ PERINGATAN: Data saat ini akan ditimpa!")
}

func processRestoreFile(bot *tgbotapi.BotAPI, msg *tgbotapi.Message, config *BotConfig) {
    chatID := msg.Chat.ID
    userID := msg.From.ID

    resetState(userID)
    sendMessage(bot, chatID, "⏳ Sedang memproses file...")

    fileID := msg.Document.FileID
    file, err := bot.GetFile(tgbotapi.FileConfig{FileID: fileID})
    if err != nil {
        replyError(bot, chatID, "Gagal mengunduh file.")
        return
    }

    fileUrl := file.Link(config.BotToken)
    resp, err := http.Get(fileUrl)
    if err != nil {
        replyError(bot, chatID, "Gagal mengunduh file content.")
        return
    }
    defer resp.Body.Close()

    body, err := ioutil.ReadAll(resp.Body)
    if err != nil {
        replyError(bot, chatID, "Gagal membaca file.")
        return
    }

    zipReader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
    if err != nil {
        replyError(bot, chatID, "File bukan format ZIP yang valid.")
        return
    }

    for _, f := range zipReader.File {
        validFiles := map[string]bool{
            "config.json":    true,
            "users.json":     true,
            "bot-config.json": true,
            "domain":         true,
            "apikey":         true,
        }

        if !validFiles[f.Name] {
            continue
        }

        rc, err := f.Open()
        if err != nil {
            continue
        }
        defer rc.Close()

        dstPath := filepath.Join("/etc/zivpn", f.Name)
        dst, err := os.Create(dstPath)
        if err != nil {
            continue
        }
        defer dst.Close()

        io.Copy(dst, rc)
    }

    exec.Command("systemctl", "restart", "zivpn").Run()
    exec.Command("systemctl", "restart", "zivpn-api").Run()

    msgSuccess := tgbotapi.NewMessage(chatID, "✅ Restore Berhasil!\nService ZiVPN, API, dan Bot telah direstart.")
    bot.Send(msgSuccess)

    go func() {
        time.Sleep(2 * time.Second)
        exec.Command("systemctl", "restart", "zivpn-bot").Run()
    }()

    showMainMenu(bot, chatID, config)
}

// ==========================================
// UI & Helpers
// ==========================================

func showMainMenu(bot *tgbotapi.BotAPI, chatID int64, config *BotConfig) {
    ipInfo, _ := getIpInfo()
    domain := config.Domain
    if domain == "" {
        domain = "(Not Configured)"
    }

    // Fetch total users
    totalUsers := 0
    users, err := getUsers()
    if err == nil {
        totalUsers = len(users)
    }

    // Enhanced UI with Total Account
    msgText := fmt.Sprintf(
            "╔══════════════════════════════╗\n"+
            "║   🤖 RISWAN JABAR STORE  ZIVPN UDP BOT   ║\n"+
            "╠══════════════════════════════╣\n"+
            "║ 📊 Total  : `%d` Akun\n"+
            "║ 🌐 Domain : `%s`\n"+
            "║ 🏙️ City   : `%s`\n"+
            "║ 📡 ISP    : `%s`\n"+
            "╚══════════════════════════════╝\n\n"+
            "👇 Silakan pilih menu dibawah ini:",
        totalUsers, domain, ipInfo.City, ipInfo.Isp,
    )

    msg := tgbotapi.NewMessage(chatID, msgText)
    msg.ReplyMarkup = getMainMenuKeyboard(config, chatID)
    sendAndTrack(bot, msg)
}

func getMainMenuKeyboard(config *BotConfig, userID int64) tgbotapi.InlineKeyboardMarkup {
    // Public Menu
    rows := [][]tgbotapi.InlineKeyboardButton{
        tgbotapi.NewInlineKeyboardRow(
            tgbotapi.NewInlineKeyboardButtonData("✨ Create Account", "menu_create"),
            tgbotapi.NewInlineKeyboardButtonData("🎁 Trial Account", "menu_trial"),
        ),
        tgbotapi.NewInlineKeyboardRow(
            tgbotapi.NewInlineKeyboardButtonData("🗑️ Delete Password", "menu_delete"),
            tgbotapi.NewInlineKeyboardButtonData("🔄 Renew Password", "menu_renew"),
        ),
    }

    // Admin Menu
    if userID == config.AdminID {
        modeLabel := "🔐 Mode: Private"
        if config.Mode == "public" {
            modeLabel = "🌍 Mode: Public"
        }

        rows = append(rows, tgbotapi.NewInlineKeyboardRow(
            tgbotapi.NewInlineKeyboardButtonData("📋 List Passwords", "menu_list"),
        ))

        rows = append(rows, tgbotapi.NewInlineKeyboardRow(
            tgbotapi.NewInlineKeyboardButtonData("📊 System Info", "menu_info"),
            tgbotapi.NewInlineKeyboardButtonData("💾 Backup & Restore", "menu_backup_restore"),
        ))
        rows = append(rows, tgbotapi.NewInlineKeyboardRow(
            tgbotapi.NewInlineKeyboardButtonData("🧹 Cleanup Expired", "menu_cleanup"),
            tgbotapi.NewInlineKeyboardButtonData(modeLabel, "toggle_mode"),
        ))
    }

    return tgbotapi.NewInlineKeyboardMarkup(rows...)
}

func sendAccountInfo(bot *tgbotapi.BotAPI, chatID int64, data map[string]interface{}, config *BotConfig) {
    ipInfo, _ := getIpInfo()
    domain := config.Domain
    if domain == "" {
        domain = "(Not Configured)"
    }

    // Safely extract values with defaults
    password := data["password"]
    expired := data["expired"]

    // Handle ip_limit and quota which might be missing in old API responses
    ipLimit := "2"
    if val, ok := data["ip_limit"]; ok {
        ipLimit = fmt.Sprintf("%v", val)
    }

    quota := "Unlimited"
    if val, ok := data["quota"]; ok && val != 0 {
        quota = fmt.Sprintf("%v GB", val)
    }

    msg := fmt.Sprintf(
        "╔══════════════════════╗\n"+
            "║ 🌐 ZIVPN UDP ACCOUNT ║\n"+
            "╠══════════════════════╣\n"+
            "║ 👤 User   : `%v`\n"+
            "║ 🔑 Pass   : `%v`\n"+
            "╠══════════════════════╣\n"+
            "║ 🌐 Domain : `%s`\n"+
            "║ 📡 IP     : `%s`\n"+
            "╠══════════════════════╣\n"+
            "║ 📅 Expired: `%v`\n"+
            "║ 📱 Limit  : `%s Device`\n"+
            "║ 💾 Quota  : `%s`\n"+
            "╠══════════════════════╣\n"+
            "║ 📍 ISP    : `%s`\n"+
            "║ 🏙️ City   : `%s`\n"+
            "╚══════════════════════╝",
        password, password, domain, ipInfo.Query, expired, ipLimit, quota, ipInfo.Isp, ipInfo.City,
    )

    reply := tgbotapi.NewMessage(chatID, msg)
    deleteLastMessage(bot, chatID)
    bot.Send(reply)
    showMainMenu(bot, chatID, config)
}

func showUserSelection(bot *tgbotapi.BotAPI, chatID int64, page int, action string) {
    users, err := getUsers()
    if err != nil {
        replyError(bot, chatID, "Gagal mengambil data user.")
        return
    }

    if len(users) == 0 {
        sendMessage(bot, chatID, "📂 Tidak ada user.")
        return
    }

    perPage := 10
    totalPages := (len(users) + perPage - 1) / perPage

    if page < 1 {
        page = 1
    }
    if page > totalPages {
        page = totalPages
    }

    start := (page - 1) * perPage
    end := start + perPage
    if end > len(users) {
        end = len(users)
    }

    var rows [][]tgbotapi.InlineKeyboardButton
    for _, u := range users[start:end] {
        label := fmt.Sprintf("%s (%s)", u.Password, u.Status)
        if u.Status == "Expired" {
            label = fmt.Sprintf("🔴 %s", label)
        } else {
            label = fmt.Sprintf("🟢 %s", label)
        }
        data := fmt.Sprintf("select_%s:%s", action, u.Password)
        rows = append(rows, tgbotapi.NewInlineKeyboardRow(
            tgbotapi.NewInlineKeyboardButtonData(label, data),
        ))
    }

    var navRow []tgbotapi.InlineKeyboardButton
    if page > 1 {
        navRow = append(navRow, tgbotapi.NewInlineKeyboardButtonData("⬅️ Prev", fmt.Sprintf("page_%s:%d", action, page-1)))
    }
    if page < totalPages {
        navRow = append(navRow, tgbotapi.NewInlineKeyboardButtonData("Next ➡️", fmt.Sprintf("page_%s:%d", action, page+1)))
    }
    if len(navRow) > 0 {
        rows = append(rows, navRow)
    }

    rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("❌ Batal", "cancel")))

    msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("📋 Pilih User untuk %s (Halaman %d/%d):", strings.Title(action), page, totalPages))
    msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(rows...)
    sendAndTrack(bot, msg)
}

func sendMessage(bot *tgbotapi.BotAPI, chatID int64, text string) {
    msg := tgbotapi.NewMessage(chatID, text)
    if _, inState := userStates[chatID]; inState {
        cancelKb := tgbotapi.NewInlineKeyboardMarkup(
            tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("❌ Batal", "cancel")),
        )
        msg.ReplyMarkup = cancelKb
    }
    sendAndTrack(bot, msg)
}

func replyError(bot *tgbotapi.BotAPI, chatID int64, text string) {
    sendMessage(bot, chatID, "❌ "+text)
}

func sendAndTrack(bot *tgbotapi.BotAPI, msg tgbotapi.MessageConfig) {
    deleteLastMessage(bot, msg.ChatID)
    sentMsg, err := bot.Send(msg)
    if err == nil {
        lastMessageIDs[msg.ChatID] = sentMsg.MessageID
    }
}

func deleteLastMessage(bot *tgbotapi.BotAPI, chatID int64) {
    if msgID, ok := lastMessageIDs[chatID]; ok {
        deleteMsg := tgbotapi.NewDeleteMessage(chatID, msgID)
        bot.Request(deleteMsg)
        delete(lastMessageIDs, chatID)
    }
}

func resetState(userID int64) {
    delete(userStates, userID)
    delete(tempUserData, userID)
}

// ==========================================
// Validation Helpers
// ==========================================

func validateUsername(bot *tgbotapi.BotAPI, chatID int64, text string) bool {
    if len(text) < 3 || len(text) > 20 {
        sendMessage(bot, chatID, "❌ Password harus 3-20 karakter. Coba lagi:")
        return false
    }
    if !regexp.MustCompile(`^[a-zA-Z0-9_-]+$`).MatchString(text) {
        sendMessage(bot, chatID, "❌ Password hanya boleh huruf, angka, - dan _. Coba lagi:")
        return false
    }
    return true
}

func validateNumber(bot *tgbotapi.BotAPI, chatID int64, text string, min, max int, fieldName string) (int, bool) {
    val, err := strconv.Atoi(text)
    if err != nil || val < min || val > max {
        sendMessage(bot, chatID, fmt.Sprintf("❌ %s harus angka positif (%d-%d). Coba lagi:", fieldName, min, max))
        return 0, false
    }
    return val, true
}

// ==========================================
// Configuration & Utils
// ==========================================

func isAllowed(config *BotConfig, userID int64) bool {
    return config.Mode == "public" || userID == config.AdminID
}

func saveConfig(config *BotConfig) error {
    data, err := json.MarshalIndent(config, "", "  ")
    if err != nil {
        return err
    }
    return ioutil.WriteFile(BotConfigFile, data, 0644)
}

func loadConfig() (BotConfig, error) {
    var config BotConfig
    file, err := ioutil.ReadFile(BotConfigFile)
    if err != nil {
        return config, err
    }
    err = json.Unmarshal(file, &config)

    if config.Domain == "" {
        if domainBytes, err := ioutil.ReadFile(DomainFile); err == nil {
            config.Domain = strings.TrimSpace(string(domainBytes))
        }
    }

    return config, err
}

// ==========================================
// API Client
// ==========================================

func apiCall(method, endpoint string, payload interface{}) (map[string]interface{}, error) {
    var reqBody []byte
    var err error

    if payload != nil {
        reqBody, err = json.Marshal(payload)
        if err != nil {
            return nil, err
        }
    }

    client := &http.Client{Timeout: 10 * time.Second} // Added timeout
    req, err := http.NewRequest(method, ApiUrl+endpoint, bytes.NewBuffer(reqBody))
    if err != nil {
        return nil, err
    }

    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("X-API-Key", ApiKey)

    resp, err := client.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    body, _ := ioutil.ReadAll(resp.Body)
    var result map[string]interface{}
    json.Unmarshal(body, &result)

    return result, nil
}

func getIpInfo() (IpInfo, error) {
    resp, err := http.Get("http://ip-api.com/json/")
    if err != nil {
        return IpInfo{}, err
    }
    defer resp.Body.Close()

    var info IpInfo
    if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
        return IpInfo{}, err
    }
    return info, nil
}

func getUsers() ([]UserData, error) {
    res, err := apiCall("GET", "/users", nil)
    if err != nil {
        return nil, err
    }

    if res["success"] != true {
        return nil, fmt.Errorf("failed to get users")
    }

    var users []UserData
    dataBytes, _ := json.Marshal(res["data"])
    json.Unmarshal(dataBytes, &users)
    return users, nil
}