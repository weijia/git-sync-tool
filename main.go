package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
)

// RepoPair 表示一对需要同步的仓库
type RepoPair struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	SourceRepo  string   `json:"source_repo"`
	SourceToken string   `json:"source_token"`
	TargetRepo  string   `json:"target_repo"`
	TargetToken string   `json:"target_token"`
	Schedule    string   `json:"schedule"` // cron 表达式或间隔秒数
	LastSync    string   `json:"last_sync"`
	Status      string   `json:"status"`
	Logs        []string `json:"logs"`
}

// Config 存储所有配置
type Config struct {
	RepoPairs []RepoPair `json:"repo_pairs"`
}

var (
	config     Config
	configLock sync.RWMutex
	configFile = "config.json"
	stopChan   = make(map[string]chan struct{})
)

func main() {
	loadConfig()
	initScheduler()

	r := mux.NewRouter()
	r.HandleFunc("/", handleIndex).Methods("GET")
	r.HandleFunc("/api/config", getConfig).Methods("GET")
	r.HandleFunc("/api/config", importConfig).Methods("POST")
	r.HandleFunc("/api/pairs", getPairs).Methods("GET")
	r.HandleFunc("/api/pairs", addPair).Methods("POST")
	r.HandleFunc("/api/pairs/{id}", getStatus).Methods("GET")
	r.HandleFunc("/api/pairs/{id}", updatePair).Methods("PUT")
	r.HandleFunc("/api/pairs/{id}", deletePair).Methods("DELETE")
	r.HandleFunc("/api/pairs/{id}/sync", triggerSync).Methods("POST")
	r.HandleFunc("/api/pairs/{id}/status", getStatus).Methods("GET")

	fmt.Println("Server starting on :8080")
	log.Fatal(http.ListenAndServe(":8080", r))
}

func initScheduler() {
	configLock.RLock()
	defer configLock.RUnlock()

	for _, pair := range config.RepoPairs {
		if pair.Schedule != "" {
			startScheduler(pair)
		}
	}
}

func startScheduler(pair RepoPair) {
	if _, exists := stopChan[pair.ID]; exists {
		return
	}

	stop := make(chan struct{})
	stopChan[pair.ID] = stop

	go func() {
		interval, err := time.ParseDuration(pair.Schedule)
		if err != nil {
			// 尝试解析为 cron 表达式（简化版，仅支持秒级间隔）
			log.Printf("Invalid schedule for %s: %v", pair.ID, err)
			return
		}

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				syncRepo(pair)
			case <-stop:
				return
			}
		}
	}()
}

func stopScheduler(id string) {
	if stop, exists := stopChan[id]; exists {
		close(stop)
		delete(stopChan, id)
	}
}

func loadConfig() {
	data, err := ioutil.ReadFile(configFile)
	if err != nil {
		if os.IsNotExist(err) {
			config = Config{RepoPairs: []RepoPair{}}
			return
		}
		log.Fatal(err)
	}
	json.Unmarshal(data, &config)
}

func saveConfig() error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return ioutil.WriteFile(configFile, data, 0644)
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	tmpl := template.Must(template.New("index").Parse(indexHTML))
	tmpl.Execute(w, nil)
}

func getPairs(w http.ResponseWriter, r *http.Request) {
	configLock.RLock()
	defer configLock.RUnlock()
	json.NewEncoder(w).Encode(config.RepoPairs)
}

func addPair(w http.ResponseWriter, r *http.Request) {
	var pair RepoPair
	if err := json.NewDecoder(r.Body).Decode(&pair); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	pair.ID = fmt.Sprintf("%d", time.Now().UnixNano())
	pair.Status = "pending"

	configLock.Lock()
	config.RepoPairs = append(config.RepoPairs, pair)
	saveConfig()
	configLock.Unlock()

	if pair.Schedule != "" {
		startScheduler(pair)
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(pair)
}

func updatePair(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	var updatedPair RepoPair
	if err := json.NewDecoder(r.Body).Decode(&updatedPair); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	configLock.Lock()
	defer configLock.Unlock()

	for i, pair := range config.RepoPairs {
		if pair.ID == id {
			updatedPair.ID = id
			updatedPair.LastSync = pair.LastSync
			config.RepoPairs[i] = updatedPair

			// 重启调度器
			stopScheduler(id)
			if updatedPair.Schedule != "" {
				startScheduler(updatedPair)
			}

			saveConfig()
			json.NewEncoder(w).Encode(updatedPair)
			return
		}
	}

	http.Error(w, "Pair not found", http.StatusNotFound)
}

func deletePair(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	configLock.Lock()
	defer configLock.Unlock()

	for i, pair := range config.RepoPairs {
		if pair.ID == id {
			config.RepoPairs = append(config.RepoPairs[:i], config.RepoPairs[i+1:]...)
			stopScheduler(id)
			saveConfig()
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}

	http.Error(w, "Pair not found", http.StatusNotFound)
}

func triggerSync(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	configLock.RLock()
	var pair RepoPair
	for _, p := range config.RepoPairs {
		if p.ID == id {
			pair = p
			break
		}
	}
	configLock.RUnlock()

	if pair.ID == "" {
		http.Error(w, "Pair not found", http.StatusNotFound)
		return
	}

	go func() {
		syncRepo(pair)
	}()

	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"status": "sync started"})
}

func getStatus(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	configLock.RLock()
	defer configLock.RUnlock()

	for _, pair := range config.RepoPairs {
		if pair.ID == id {
			json.NewEncoder(w).Encode(pair)
			return
		}
	}

	http.Error(w, "Pair not found", http.StatusNotFound)
}

func getConfig(w http.ResponseWriter, r *http.Request) {
	configLock.RLock()
	defer configLock.RUnlock()
	json.NewEncoder(w).Encode(config)
}

func importConfig(w http.ResponseWriter, r *http.Request) {
	var newConfig Config
	if err := json.NewDecoder(r.Body).Decode(&newConfig); err != nil {
		http.Error(w, "Invalid config format: "+err.Error(), http.StatusBadRequest)
		return
	}

	configLock.Lock()
	defer configLock.Unlock()

	// 停止所有现有的调度器
	for _, pair := range config.RepoPairs {
		if pair.Schedule != "" {
			stopScheduler(pair.ID)
		}
	}

	// 更新配置
	config = newConfig

	// 启动新的调度器
	for _, pair := range config.RepoPairs {
		if pair.Schedule != "" {
			startScheduler(pair)
		}
	}

	// 保存配置
	if err := saveConfig(); err != nil {
		http.Error(w, "Failed to save config: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "success", "message": "Config imported successfully"})
}

func syncRepo(pair RepoPair) {
	updateStatus(pair.ID, "syncing", "")
	addLog(pair.ID, "开始同步")
	defer func() {
		if r := recover(); r != nil {
			errorMsg := fmt.Sprintf("%v", r)
			updateStatus(pair.ID, "error", errorMsg)
			addLog(pair.ID, "同步失败: "+errorMsg)
		}
	}()

	// 创建临时目录
	tmpDir := filepath.Join(os.TempDir(), "git-sync-"+pair.ID)
	addLog(pair.ID, "创建临时目录: "+tmpDir)
	os.RemoveAll(tmpDir)
	os.MkdirAll(tmpDir, 0755)
	defer os.RemoveAll(tmpDir)

	// 克隆源仓库
	sourceURL := getRepoURL(pair.SourceRepo, pair.SourceToken)
	targetURL := getRepoURL(pair.TargetRepo, pair.TargetToken)

	addLog(pair.ID, "开始克隆源仓库: "+pair.SourceRepo)
	addLog(pair.ID, "构建的源仓库 URL: "+sourceURL)
	addLog(pair.ID, "开始克隆...")

	// 使用系统 Git 命令克隆仓库
	cmd := exec.Command("git", "clone", sourceURL, tmpDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()

	if err != nil {
		errorMsg := fmt.Sprintf("克隆源仓库失败: %v", err)
		updateStatus(pair.ID, "error", errorMsg)
		addLog(pair.ID, errorMsg)
		addLog(pair.ID, "尝试使用 HTTP 协议...")
		// 尝试使用 HTTP 协议
		httpURL := strings.Replace(sourceURL, "https://", "http://", 1)
		addLog(pair.ID, "尝试使用 HTTP URL: "+httpURL)
		cmd = exec.Command("git", "clone", httpURL, tmpDir)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err = cmd.Run()
		if err != nil {
			errorMsg = fmt.Sprintf("HTTP 克隆也失败: %v", err)
			addLog(pair.ID, errorMsg)
			updateStatus(pair.ID, "error", errorMsg)
			return
		}
		addLog(pair.ID, "HTTP 克隆成功")
	} else {
		addLog(pair.ID, "源仓库克隆成功")
	}

	// 设置 Git 配置
	cmd = exec.Command("git", "config", "user.name", "Git Sync Tool")
	cmd.Dir = tmpDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		errorMsg := fmt.Sprintf("设置用户名失败: %v", err)
		updateStatus(pair.ID, "error", errorMsg)
		addLog(pair.ID, errorMsg)
		return
	}

	cmd = exec.Command("git", "config", "user.email", "sync@git-tool.local")
	cmd.Dir = tmpDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		errorMsg := fmt.Sprintf("设置邮箱失败: %v", err)
		updateStatus(pair.ID, "error", errorMsg)
		addLog(pair.ID, errorMsg)
		return
	}

	// 获取当前分支
	cmd = exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = tmpDir
	output, err := cmd.Output()
	if err != nil {
		errorMsg := fmt.Sprintf("获取当前分支失败: %v", err)
		updateStatus(pair.ID, "error", errorMsg)
		addLog(pair.ID, errorMsg)
		return
	}
	currentBranch := strings.TrimSpace(string(output))
	addLog(pair.ID, "当前分支: "+currentBranch)

	// 添加目标仓库作为远程
	addLog(pair.ID, "添加目标仓库作为远程...")
	cmd = exec.Command("git", "remote", "add", "target", targetURL)
	cmd.Dir = tmpDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// 远程可能已存在，尝试更新
		cmd = exec.Command("git", "remote", "set-url", "target", targetURL)
		cmd.Dir = tmpDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			errorMsg := fmt.Sprintf("添加目标仓库远程失败: %v", err)
			updateStatus(pair.ID, "error", errorMsg)
			addLog(pair.ID, errorMsg)
			return
		}
	}

	// 拉取目标仓库的最新变更
	addLog(pair.ID, "拉取目标仓库的最新变更...")
	cmd = exec.Command("git", "fetch", "target")
	cmd.Dir = tmpDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// 忽略拉取失败的错误，可能是因为目标仓库为空
		addLog(pair.ID, "拉取目标仓库失败（可能是空仓库）: "+err.Error())
	}

	// 合并目标仓库的变更到当前分支
	addLog(pair.ID, "合并目标仓库的变更...")
	cmd = exec.Command("git", "merge", "target/"+currentBranch, "--allow-unrelated-histories", "--strategy-option=ours")
	cmd.Dir = tmpDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// 尝试合并 master 分支
		cmd = exec.Command("git", "merge", "target/master", "--allow-unrelated-histories", "--strategy-option=ours")
		cmd.Dir = tmpDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			// 尝试合并 main 分支
			cmd = exec.Command("git", "merge", "target/main", "--allow-unrelated-histories", "--strategy-option=ours")
			cmd.Dir = tmpDir
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				// 忽略合并失败的错误，可能是因为目标仓库为空
				addLog(pair.ID, "合并目标仓库变更失败（可能是空仓库）: "+err.Error())
			}
		}
	}

	// 推送合并后的变更到目标仓库
	addLog(pair.ID, "推送合并后的变更到目标仓库...")
	cmd = exec.Command("git", "push", "target", currentBranch+":"+currentBranch)
	cmd.Dir = tmpDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// 尝试推送 master 分支
		cmd = exec.Command("git", "push", "target", currentBranch+":master")
		cmd.Dir = tmpDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			// 尝试推送 main 分支
			cmd = exec.Command("git", "push", "target", currentBranch+":main")
			cmd.Dir = tmpDir
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				errorMsg := fmt.Sprintf("推送目标仓库失败: %v", err)
				updateStatus(pair.ID, "error", errorMsg)
				addLog(pair.ID, errorMsg)
				return
			}
		}
	}
	addLog(pair.ID, "目标仓库推送成功")

	// 推送合并后的变更到源仓库
	addLog(pair.ID, "推送合并后的变更到源仓库...")
	cmd = exec.Command("git", "push", "origin", currentBranch+":"+currentBranch)
	cmd.Dir = tmpDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		errorMsg := fmt.Sprintf("推送源仓库失败: %v", err)
		updateStatus(pair.ID, "error", errorMsg)
		addLog(pair.ID, errorMsg)
		return
	}
	addLog(pair.ID, "源仓库推送成功")

	successTime := time.Now().Format(time.RFC3339)
	updateStatus(pair.ID, "success", successTime)
	addLog(pair.ID, "双向同步完成: "+successTime)
}

func getRepoURL(repo, token string) string {
	if strings.Contains(repo, "@") {
		// 确保 URL 有 .git 后缀
		if !strings.HasSuffix(repo, ".git") {
			repo += ".git"
		}
		return repo
	}
	// 假设是 GitHub 仓库
	parts := strings.Split(repo, "/")
	if len(parts) == 2 {
		return fmt.Sprintf("https://%s@github.com/%s.git", token, repo)
	}
	// 确保完整 URL 有 .git 后缀
	if !strings.HasSuffix(repo, ".git") {
		repo += ".git"
	}
	return repo
}

func updateStatus(id, status, lastSync string) {
	configLock.Lock()
	defer configLock.Unlock()

	for i, pair := range config.RepoPairs {
		if pair.ID == id {
			config.RepoPairs[i].Status = status
			if lastSync != "" {
				config.RepoPairs[i].LastSync = lastSync
			}
			saveConfig()
			return
		}
	}
}

func addLog(id, message string) {
	configLock.Lock()
	defer configLock.Unlock()

	for i, pair := range config.RepoPairs {
		if pair.ID == id {
			// 限制日志数量，只保留最近的 100 条
			logs := config.RepoPairs[i].Logs
			logs = append(logs, time.Now().Format("2006-01-02 15:04:05")+": "+message)
			if len(logs) > 100 {
				logs = logs[len(logs)-100:]
			}
			config.RepoPairs[i].Logs = logs
			saveConfig()
			return
		}
	}
}

const indexHTML = `<!DOCTYPE html>
<html>
<head>
    <title>Git Sync Tool</title>
    <style>
        body { font-family: Arial, sans-serif; margin: 40px; }
        .form-group { margin-bottom: 15px; }
        label { display: block; margin-bottom: 5px; font-weight: bold; }
        input, select { width: 100%; padding: 8px; box-sizing: border-box; }
        button { background: #007bff; color: white; padding: 10px 20px; border: none; cursor: pointer; }
        button:hover { background: #0056b3; }
        table { width: 100%; border-collapse: collapse; margin-top: 30px; }
        th, td { border: 1px solid #ddd; padding: 12px; text-align: left; }
        th { background: #f4f4f4; }
        .status-success { color: green; }
        .status-error { color: red; }
        .status-syncing { color: orange; }
    </style>
</head>
<body>
    <h1>🔄 Git Repo Sync Tool</h1>
    
    <div id="form">
        <h2>Add/Edit Repo Pair</h2>
        <input type="hidden" id="pairId">
        <div class="form-group">
            <label>Name:</label>
            <input type="text" id="name" required>
        </div>
        <div class="form-group">
            <label>Source Repo (owner/repo):</label>
            <input type="text" id="sourceRepo" required>
        </div>
        <div class="form-group">
            <label>Source Token:</label>
            <input type="password" id="sourceToken" required>
        </div>
        <div class="form-group">
            <label>Target Repo (owner/repo):</label>
            <input type="text" id="targetRepo" required>
        </div>
        <div class="form-group">
            <label>Target Token:</label>
            <input type="password" id="targetToken" required>
        </div>
        <div class="form-group">
            <label>Sync Schedule (e.g., 1h, 30m, 3600s):</label>
            <input type="text" id="schedule" placeholder="Leave empty for manual only">
        </div>


        <button onclick="savePair()">Save</button>
        <button onclick="clearForm()" style="background:#6c757d">Clear</button>
    </div>

    <h2>Repo Pairs</h2>
        <div style="margin-bottom:15px">
            <button onclick="exportConfig()" style="background:#ffc107; color:black; margin-right:10px">Export Config</button>
            <input type="file" id="configFile" accept=".json" style="display:none">
            <button onclick="document.getElementById('configFile').click()" style="background:#28a745; color:white">Import Config</button>
        </div>
    <table>
        <thead>
            <tr>
                <th>Name</th>
                <th>Source -&gt; Target</th>
                <th>Schedule</th>
                <th>Last Sync</th>
                <th>Status</th>
                <th>Actions</th>
            </tr>
        </thead>
        <tbody id="pairsTable"></tbody>
    </table>

    <script>
        async function loadPairs() {
            const res = await fetch('/api/pairs');
            const pairs = await res.json();
            const tbody = document.getElementById('pairsTable');
            tbody.innerHTML = pairs.map(p => {
                return '<tr>' +
                    '<td>' + p.name + '</td>' +
                    '<td>' + p.source_repo + ' -&gt; ' + p.target_repo + '</td>' +
                    '<td>' + (p.schedule || 'Manual') + '</td>' +
                    '<td>' + (p.last_sync || 'Never') + '</td>' +
                    '<td class="status-' + p.status + '">' + p.status + '</td>' +
                    '<td>' +
                        '<button onclick="editPair(\'' + p.id + '\')">Edit</button>' +
                        '<button onclick="triggerSync(\'' + p.id + '\')" style="background:#28a745">Sync Now</button>' +
                        '<button onclick="showLogs(\'' + p.id + '\')" style="background:#17a2b8">Logs</button>' +
                        '<button onclick="deletePair(\'' + p.id + '\')" style="background:#dc3545">Delete</button>' +
                    '</td>' +
                '</tr>';
            }).join('');
        }

        async function savePair() {
            const data = {
                name: document.getElementById('name').value,
                source_repo: document.getElementById('sourceRepo').value,
                source_token: document.getElementById('sourceToken').value,
                target_repo: document.getElementById('targetRepo').value,
                target_token: document.getElementById('targetToken').value,
                schedule: document.getElementById('schedule').value
            };

            const id = document.getElementById('pairId').value;
            const url = id ? '/api/pairs/' + id : '/api/pairs';
            const method = id ? 'PUT' : 'POST';

            await fetch(url, {
                method,
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify(data)
            });

            clearForm();
            loadPairs();
        }

        async function editPair(id) {
            const res = await fetch('/api/pairs/' + id);
            const p = await res.json();
            document.getElementById('pairId').value = p.id;
            document.getElementById('name').value = p.name;
            document.getElementById('sourceRepo').value = p.source_repo;
            document.getElementById('sourceToken').value = p.source_token;
            document.getElementById('targetRepo').value = p.target_repo;
            document.getElementById('targetToken').value = p.target_token;
            document.getElementById('schedule').value = p.schedule || '';
        }

        async function deletePair(id) {
            if (!confirm('Delete this repo pair?')) return;
            await fetch('/api/pairs/' + id, {method: 'DELETE'});
            loadPairs();
        }

        async function triggerSync(id) {
            await fetch('/api/pairs/' + id + '/sync', {method: 'POST'});
            alert('Sync started!');
            loadPairs();
        }

        function clearForm() {
            document.getElementById('pairId').value = '';
            document.querySelectorAll('input').forEach(i => {
                if (i.type !== 'checkbox') i.value = '';
                else i.checked = false;
            });
        }

        async function showLogs(id) {
            const res = await fetch('/api/pairs/' + id);
            const p = await res.json();
            const logs = p.logs || [];
            const logContent = logs.map(log => '<div>' + log + '</div>').join('');
            
            const modal = document.createElement('div');
            modal.style.position = 'fixed';
            modal.style.top = '0';
            modal.style.left = '0';
            modal.style.width = '100%';
            modal.style.height = '100%';
            modal.style.backgroundColor = 'rgba(0, 0, 0, 0.5)';
            modal.style.display = 'flex';
            modal.style.justifyContent = 'center';
            modal.style.alignItems = 'center';
            modal.style.zIndex = '1000';
            
            const modalContent = document.createElement('div');
            modalContent.style.backgroundColor = 'white';
            modalContent.style.padding = '20px';
            modalContent.style.borderRadius = '5px';
            modalContent.style.width = '80%';
            modalContent.style.maxHeight = '80%';
            modalContent.style.overflow = 'auto';
            
            const modalHeader = document.createElement('div');
            modalHeader.style.display = 'flex';
            modalHeader.style.justifyContent = 'space-between';
            modalHeader.style.alignItems = 'center';
            modalHeader.style.marginBottom = '15px';
            
            const modalTitle = document.createElement('h3');
            modalTitle.textContent = 'Sync Logs for ' + p.name;
            
            const closeButton = document.createElement('button');
            closeButton.textContent = 'Close';
            closeButton.style.background = '#6c757d';
            closeButton.style.color = 'white';
            closeButton.style.border = 'none';
            closeButton.style.padding = '5px 10px';
            closeButton.style.borderRadius = '3px';
            closeButton.style.cursor = 'pointer';
            closeButton.onclick = () => {
                document.body.removeChild(modal);
            };
            
            modalHeader.appendChild(modalTitle);
            modalHeader.appendChild(closeButton);
            
            const logContainer = document.createElement('div');
            logContainer.innerHTML = logContent || '<div>No logs available</div>';
            logContainer.style.fontFamily = 'monospace';
            logContainer.style.fontSize = '14px';
            logContainer.style.lineHeight = '1.5';
            
            modalContent.appendChild(modalHeader);
            modalContent.appendChild(logContainer);
            modal.appendChild(modalContent);
            
            document.body.appendChild(modal);
        }

        async function exportConfig() {
            const res = await fetch('/api/config');
            const config = await res.json();
            const configJson = JSON.stringify(config, null, 2);
            
            const blob = new Blob([configJson], { type: 'application/json' });
            const url = URL.createObjectURL(blob);
            
            const a = document.createElement('a');
            a.href = url;
            a.download = 'git-sync-config.json';
            a.click();
            
            URL.revokeObjectURL(url);
        }

        async function importConfig(file) {
            const reader = new FileReader();
            reader.onload = async function(e) {
                try {
                    const config = JSON.parse(e.target.result);
                    const res = await fetch('/api/config', {
                        method: 'POST',
                        headers: {
                            'Content-Type': 'application/json'
                        },
                        body: JSON.stringify(config)
                    });
                    
                    if (res.ok) {
                        alert('配置导入成功！');
                        loadPairs();
                    } else {
                        alert('配置导入失败：' + await res.text());
                    }
                } catch (error) {
                    alert('配置文件格式错误：' + error.message);
                }
            };
            reader.readAsText(file);
        }

        // 添加文件选择事件监听器
        document.getElementById('configFile').addEventListener('change', function(e) {
            if (e.target.files.length > 0) {
                importConfig(e.target.files[0]);
                // 重置文件输入，以便可以再次选择同一个文件
                e.target.value = '';
            }
        });

        loadPairs();
    </script>
</body>
</html>`
