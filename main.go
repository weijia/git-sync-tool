package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/gorilla/mux"
)

// RepoPair 表示一对需要同步的仓库
type RepoPair struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	SourceRepo  string `json:"source_repo"`
	SourceToken string `json:"source_token"`
	TargetRepo  string `json:"target_repo"`
	TargetToken string `json:"target_token"`
	Schedule    string `json:"schedule"` // cron 表达式或间隔秒数
	LastSync    string `json:"last_sync"`
	Status      string `json:"status"`
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
	r.HandleFunc("/api/pairs", getPairs).Methods("GET")
	r.HandleFunc("/api/pairs", addPair).Methods("POST")
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

func syncRepo(pair RepoPair) {
	updateStatus(pair.ID, "syncing", "")
	defer func() {
		if r := recover(); r != nil {
			updateStatus(pair.ID, "error", fmt.Sprintf("%v", r))
		}
	}()

	// 创建临时目录
	tmpDir := filepath.Join(os.TempDir(), "git-sync-"+pair.ID)
	os.RemoveAll(tmpDir)
	os.MkdirAll(tmpDir, 0755)
	defer os.RemoveAll(tmpDir)

	// 克隆源仓库
	sourceURL := getRepoURL(pair.SourceRepo, pair.SourceToken)
	_, err := git.PlainClone(tmpDir, false, &git.CloneOptions{
		URL:      sourceURL,
		Progress: os.Stdout,
	})
	if err != nil {
		updateStatus(pair.ID, "error", fmt.Sprintf("Clone failed: %v", err))
		return
	}

	// 提交并推送到目标仓库
	err = pushToTarget(tmpDir, pair)
	if err != nil {
		updateStatus(pair.ID, "error", fmt.Sprintf("Push failed: %v", err))
		return
	}

	updateStatus(pair.ID, "success", time.Now().Format(time.RFC3339))
}

func getRepoURL(repo, token string) string {
	if strings.Contains(repo, "@") {
		return repo
	}
	// 假设是 GitHub 仓库
	parts := strings.Split(repo, "/")
	if len(parts) == 2 {
		return fmt.Sprintf("https://%s@github.com/%s.git", token, repo)
	}
	return repo
}



func pushToTarget(repoDir string, pair RepoPair) error {
	repo, err := git.PlainOpen(repoDir)
	if err != nil {
		return err
	}

	// 创建提交
	worktree, err := repo.Worktree()
	if err != nil {
		return err
	}

	_, err = worktree.Add(".")
	if err != nil {
		return err
	}

	_, err = worktree.Commit("Sync from "+pair.SourceRepo, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Git Sync Tool",
			Email: "sync@git-tool.local",
			When:  time.Now(),
		},
	})
	if err != nil && err != git.NoErrAlreadyUpToDate {
		return err
	}

	// 添加远程并推送
	targetURL := getRepoURL(pair.TargetRepo, pair.TargetToken)
	_, err = repo.CreateRemote(&gitconfig.RemoteConfig{
		Name: "target",
		URLs: []string{targetURL},
	})
	if err != nil {
		// 远程可能已存在
		remote, _ := repo.Remote("target")
		if remote != nil {
			repo.DeleteRemote("target")
			_, err = repo.CreateRemote(&gitconfig.RemoteConfig{
				Name: "target",
				URLs: []string{targetURL},
			})
		}
	}

	err = repo.Push(&git.PushOptions{
		RemoteName: "target",
		Force:      true,
		Progress:   os.Stdout,
	})
	if err != nil {
		return err
	}

	return nil
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
    <table>
        <thead>
            <tr>
                <th>Name</th>
                <th>Source → Target</th>
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

        loadPairs();
    </script>
</body>
</html>`
