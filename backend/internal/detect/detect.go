// Package detect performs lightweight static analysis of a cloned repository
// to determine the application's language, framework, runtime version, and
// the correct build / start commands to run inside a Podman container.
package detect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Result holds the detected application metadata.
type Result struct {
	Language     string `json:"language"`      // "nodejs"|"python"|"php"|"static"|"unknown"
	Framework    string `json:"framework"`     // e.g. "nextjs", "flask", "laravel"
	Version      string `json:"version"`       // runtime version hint, e.g. "20", "3.12", "8.2"
	BuildCommand string `json:"build_command"` // e.g. "npm ci && npm run build"
	StartCommand string `json:"start_command"` // e.g. "node server.js"
	AppPort      int    `json:"app_port"`      // default port the app listens on
	BaseImage    string `json:"base_image"`    // suggested OCI base image for Podman
}

// Detect inspects the directory at root and returns the best-effort Result.
// Detection is attempted in order: Node.js → Python → PHP → Static → Unknown.
func Detect(root string) *Result {
	if r := detectNode(root); r != nil {
		return r
	}
	if r := detectPython(root); r != nil {
		return r
	}
	if r := detectPHP(root); r != nil {
		return r
	}
	if r := detectStatic(root); r != nil {
		return r
	}
	return &Result{
		Language:     "unknown",
		Framework:    "unknown",
		AppPort:      8080,
		BaseImage:    "alpine:3.19",
		BuildCommand: "",
		StartCommand: "./app",
	}
}

// ─── Node.js ─────────────────────────────────────────────────────────────────

type packageJSON struct {
	Scripts struct {
		Build string `json:"build"`
		Start string `json:"start"`
		Dev   string `json:"dev"`
	} `json:"scripts"`
	Engines struct {
		Node string `json:"node"`
	} `json:"engines"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

func detectNode(root string) *Result {
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return nil
	}
	var pkg packageJSON
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil
	}
	r := &Result{Language: "nodejs", AppPort: 3000, BaseImage: "node:20-alpine"}
	r.Version = detectNodeVersion(root, pkg.Engines.Node)

	// Merge deps + devDeps for framework detection
	deps := make(map[string]bool, len(pkg.Dependencies)+len(pkg.DevDependencies))
	for k := range pkg.Dependencies {
		deps[k] = true
	}
	for k := range pkg.DevDependencies {
		deps[k] = true
	}

	// Framework detection (most specific first)
	switch {
	case deps["next"]:
		r.Framework = "nextjs"
		r.BuildCommand = "npm ci && npm run build"
		r.StartCommand = "npm start"
	case deps["nuxt"] || deps["nuxt3"] || deps["@nuxt/core"]:
		r.Framework = "nuxt"
		r.BuildCommand = "npm ci && npm run build"
		r.StartCommand = "node .output/server/index.mjs"
	case deps["gatsby"]:
		r.Framework = "gatsby"
		r.BuildCommand = "npm ci && npm run build"
		r.StartCommand = "npm run serve"
	case deps["@remix-run/node"] || deps["@remix-run/react"]:
		r.Framework = "remix"
		r.BuildCommand = "npm ci && npm run build"
		r.StartCommand = "npm start"
	case deps["@sveltejs/kit"] || deps["svelte"]:
		r.Framework = "svelte"
		r.BuildCommand = "npm ci && npm run build"
		r.StartCommand = "node build"
	case deps["@angular/core"]:
		r.Framework = "angular"
		r.BuildCommand = "npm ci && npx ng build --configuration=production"
		r.StartCommand = "npx serve -s dist -l 3000"
	case deps["vue"]:
		r.Framework = "vue"
		r.BuildCommand = "npm ci && npm run build"
		r.StartCommand = "npx serve -s dist -l 3000"
	case deps["react"]:
		r.Framework = "react"
		r.BuildCommand = "npm ci && npm run build"
		r.StartCommand = "npx serve -s build -l 3000"
	case deps["@nestjs/core"]:
		r.Framework = "nestjs"
		r.BuildCommand = "npm ci && npm run build"
		r.StartCommand = "node dist/main"
	case deps["fastify"]:
		r.Framework = "fastify"
		r.BuildCommand = "npm ci"
		if pkg.Scripts.Start != "" {
			r.StartCommand = "npm start"
		} else {
			r.StartCommand = "node server.js"
		}
	case deps["express"]:
		r.Framework = "express"
		r.BuildCommand = "npm ci"
		if pkg.Scripts.Start != "" {
			r.StartCommand = "npm start"
		} else {
			r.StartCommand = "node index.js"
		}
	case deps["koa"]:
		r.Framework = "koa"
		r.BuildCommand = "npm ci"
		r.StartCommand = "node app.js"
	case deps["hapi"] || deps["@hapi/hapi"]:
		r.Framework = "hapi"
		r.BuildCommand = "npm ci"
		r.StartCommand = "node index.js"
	default:
		r.Framework = "nodejs"
		if pkg.Scripts.Build != "" {
			r.BuildCommand = "npm ci && npm run build"
		} else {
			r.BuildCommand = "npm ci"
		}
		if pkg.Scripts.Start != "" {
			r.StartCommand = "npm start"
		} else {
			r.StartCommand = "node index.js"
		}
	}

	// Override build/start from scripts if we haven't set them specifically
	if r.BuildCommand == "npm ci" && pkg.Scripts.Build != "" {
		r.BuildCommand = "npm ci && npm run build"
	}

	// Use yarn or pnpm if lock file found
	if _, err := os.Stat(filepath.Join(root, "yarn.lock")); err == nil {
		r.BuildCommand = strings.ReplaceAll(r.BuildCommand, "npm ci", "yarn install --frozen-lockfile")
		r.StartCommand = strings.ReplaceAll(r.StartCommand, "npm start", "yarn start")
	} else if _, err := os.Stat(filepath.Join(root, "pnpm-lock.yaml")); err == nil {
		r.BuildCommand = strings.ReplaceAll(r.BuildCommand, "npm ci", "pnpm install --frozen-lockfile")
		r.StartCommand = strings.ReplaceAll(r.StartCommand, "npm start", "pnpm start")
	}

	return r
}

func detectNodeVersion(root, engines string) string {
	if data, err := os.ReadFile(filepath.Join(root, ".nvmrc")); err == nil {
		ver := strings.TrimSpace(strings.TrimPrefix(string(data), "v"))
		if ver != "" {
			return ver
		}
	}
	if data, err := os.ReadFile(filepath.Join(root, ".node-version")); err == nil {
		ver := strings.TrimSpace(strings.TrimPrefix(string(data), "v"))
		if ver != "" {
			return ver
		}
	}
	if engines != "" {
		// Strip constraint chars, take first token
		parts := strings.FieldsFunc(engines, func(ch rune) bool {
			return ch == '>' || ch == '<' || ch == '=' || ch == '^' || ch == '~' || ch == ' '
		})
		if len(parts) > 0 {
			return parts[0]
		}
	}
	return "20"
}

// ─── Python ──────────────────────────────────────────────────────────────────

func detectPython(root string) *Result {
	var depsContent string
	found := false
	for _, f := range []string{"requirements.txt", "Pipfile", "pyproject.toml", "setup.py", "setup.cfg"} {
		if data, err := os.ReadFile(filepath.Join(root, f)); err == nil {
			depsContent += strings.ToLower(string(data)) + "\n"
			found = true
		}
	}
	if !found {
		return nil
	}

	r := &Result{Language: "python", AppPort: 8000, BaseImage: "python:3.12-slim"}
	r.Version = detectPythonVersion(root)

	// Detect entry point for Flask
	flaskEntry := "app"
	for _, f := range []string{"app.py", "main.py", "run.py", "wsgi.py", "server.py", "application.py"} {
		if _, err := os.Stat(filepath.Join(root, f)); err == nil {
			flaskEntry = strings.TrimSuffix(f, ".py")
			break
		}
	}

	switch {
	case strings.Contains(depsContent, "fastapi"):
		r.Framework = "fastapi"
		r.BuildCommand = "pip install --no-cache-dir -r requirements.txt"
		appModule := detectPythonModule(root, "app", "main", "server", "application", "api")
		r.StartCommand = "uvicorn " + appModule + ":app --host 0.0.0.0 --port 8000"
	case strings.Contains(depsContent, "django"):
		r.Framework = "django"
		// collectstatic requires DJANGO_SETTINGS_MODULE and a working DB;
		// run it only if it can succeed (|| true prevents build failure).
		r.BuildCommand = "pip install --no-cache-dir -r requirements.txt && python manage.py collectstatic --noinput || true"
		wsgiModule := findDjangoWsgiModule(root)
		r.StartCommand = "gunicorn --bind 0.0.0.0:8000 --workers 2 " + wsgiModule
	case strings.Contains(depsContent, "flask"):
		r.Framework = "flask"
		r.BuildCommand = "pip install --no-cache-dir -r requirements.txt"
		appVar := detectFlaskAppVar(root, flaskEntry+".py")
		r.StartCommand = "gunicorn --bind 0.0.0.0:8000 " + flaskEntry + ":" + appVar
	case strings.Contains(depsContent, "starlette"):
		r.Framework = "starlette"
		r.BuildCommand = "pip install --no-cache-dir -r requirements.txt"
		appModule := detectPythonModule(root, "main", "app", "server")
		r.StartCommand = "uvicorn " + appModule + ":app --host 0.0.0.0 --port 8000"
	case strings.Contains(depsContent, "tornado"):
		r.Framework = "tornado"
		r.BuildCommand = "pip install --no-cache-dir -r requirements.txt"
		r.StartCommand = "python " + detectPythonModule(root, "main", "app", "server") + ".py"
	case strings.Contains(depsContent, "aiohttp"):
		r.Framework = "aiohttp"
		r.BuildCommand = "pip install --no-cache-dir -r requirements.txt"
		r.StartCommand = "python " + detectPythonModule(root, "main", "app", "server") + ".py"
	case strings.Contains(depsContent, "bottle"):
		r.Framework = "bottle"
		r.BuildCommand = "pip install --no-cache-dir -r requirements.txt"
		r.StartCommand = "python " + detectPythonModule(root, "app", "main", "server") + ".py"
	case strings.Contains(depsContent, "sanic"):
		r.Framework = "sanic"
		r.BuildCommand = "pip install --no-cache-dir -r requirements.txt"
		r.StartCommand = "python " + detectPythonModule(root, "server", "app", "main") + ".py"
	default:
		r.Framework = "python"
		r.BuildCommand = "pip install --no-cache-dir -r requirements.txt"
		r.StartCommand = "python " + detectPythonModule(root, "main", "app", "server", "run", "manage") + ".py"
	}

	// Use Pipenv if Pipfile present
	if _, err := os.Stat(filepath.Join(root, "Pipfile")); err == nil {
		r.BuildCommand = "pip install pipenv && pipenv install --system --deploy"
	}

	return r
}

// findDjangoWsgiModule locates the wsgi.py in the project and returns the
// dotted Python module path (e.g. "myproject.wsgi:application").
// Django projects keep wsgi.py inside a package subdirectory, not at root.
func findDjangoWsgiModule(root string) string {
	// 1. Root-level wsgi.py (unusual but possible)
	if _, err := os.Stat(filepath.Join(root, "wsgi.py")); err == nil {
		return "wsgi:application"
	}
	// 2. Extract project name from manage.py DJANGO_SETTINGS_MODULE
	if data, err := os.ReadFile(filepath.Join(root, "manage.py")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, "DJANGO_SETTINGS_MODULE") {
				// e.g. 'myproject.settings'  or  "myproject.settings.base"
				for _, tok := range strings.FieldsFunc(line, func(r rune) bool {
					return r == '"' || r == '\''
				}) {
					if idx := strings.Index(tok, ".settings"); idx > 0 {
						pkg := tok[:idx]
						// Verify the package/wsgi.py actually exists
						if _, err := os.Stat(filepath.Join(root, pkg, "wsgi.py")); err == nil {
							return pkg + ".wsgi:application"
						}
						// Return the dotted path even if file not found yet (may exist after build)
						return pkg + ".wsgi:application"
					}
				}
			}
		}
	}
	// 3. Walk immediate subdirectories for wsgi.py
	if entries, err := os.ReadDir(root); err == nil {
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			switch e.Name() {
			case "venv", ".venv", "env", "__pycache__", "static", "media", "node_modules":
				continue
			}
			if _, err := os.Stat(filepath.Join(root, e.Name(), "wsgi.py")); err == nil {
				return e.Name() + ".wsgi:application"
			}
		}
	}
	return "wsgi:application"
}

// detectFlaskAppVar inspects filename in root to find the Flask application
// variable name. Handles factory pattern (create_app) and non-standard names.
func detectFlaskAppVar(root, filename string) string {
	data, err := os.ReadFile(filepath.Join(root, filename))
	if err != nil {
		return "app"
	}
	content := string(data)
	// Factory pattern: gunicorn supports "module:create_app()"
	if strings.Contains(content, "def create_app") {
		return "create_app()"
	}
	// application = Flask(
	if strings.Contains(content, "application = Flask(") || strings.Contains(content, "application=Flask(") {
		return "application"
	}
	// app = Flask(  (most common)
	if strings.Contains(content, "app = Flask(") || strings.Contains(content, "app=Flask(") {
		return "app"
	}
	return "app"
}

// detectPythonModule returns the first of candidates that exists as a .py
// file in root. Falls back to the first candidate if none are found.
func detectPythonModule(root string, candidates ...string) string {
	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(root, c+".py")); err == nil {
			return c
		}
	}
	if len(candidates) > 0 {
		return candidates[0]
	}
	return "main"
}

func detectPythonVersion(root string) string {
	if data, err := os.ReadFile(filepath.Join(root, "runtime.txt")); err == nil {
		v := strings.TrimSpace(strings.TrimPrefix(string(data), "python-"))
		if v != "" {
			return v
		}
	}
	if data, err := os.ReadFile(filepath.Join(root, ".python-version")); err == nil {
		v := strings.TrimSpace(string(data))
		if v != "" {
			return v
		}
	}
	if data, err := os.ReadFile(filepath.Join(root, "pyproject.toml")); err == nil {
		// Look for python = "^3.x" or requires-python = ">=3.x"
		for _, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, "requires-python") || strings.HasPrefix(strings.TrimSpace(line), "python =") {
				parts := strings.FieldsFunc(line, func(ch rune) bool {
					return ch == '"' || ch == '\'' || ch == '>' || ch == '<' || ch == '=' || ch == '^' || ch == '~' || ch == ' '
				})
				for _, p := range parts {
					if len(p) > 0 && (p[0] >= '2' && p[0] <= '4') {
						return p
					}
				}
			}
		}
	}
	return "3.12"
}

// ─── PHP ─────────────────────────────────────────────────────────────────────

type composerJSON struct {
	Require map[string]string `json:"require"`
}

func detectPHP(root string) *Result {
	// WordPress: look for wp-config.php or wp-login.php
	for _, wpFile := range []string{"wp-config.php", "wp-login.php", "wp-config-sample.php"} {
		if _, err := os.Stat(filepath.Join(root, wpFile)); err == nil {
			return &Result{
				Language:     "php",
				Framework:    "wordpress",
				Version:      "8.2",
				BuildCommand: "composer install --no-dev 2>/dev/null || true",
				StartCommand: "apache2-foreground",
				AppPort:      80,
				BaseImage:    "wordpress:latest",
			}
		}
	}

	data, err := os.ReadFile(filepath.Join(root, "composer.json"))
	if err != nil {
		// Bare PHP — just index.php
		if _, err2 := os.Stat(filepath.Join(root, "index.php")); err2 == nil {
			return &Result{
				Language:     "php",
				Framework:    "php",
				Version:      "8.2",
				BuildCommand: "",
				StartCommand: "php -S 0.0.0.0:8080 -t .",
				AppPort:      8080,
				BaseImage:    "php:8.2-cli-alpine",
			}
		}
		return nil
	}

	var comp composerJSON
	_ = json.Unmarshal(data, &comp)

	phpVer := detectPHPVersion(root, comp.Require)
	r := &Result{Language: "php", AppPort: 8080, BaseImage: "php:8.2-cli-alpine", Version: phpVer}

	switch {
	case comp.Require["laravel/framework"] != "":
		r.Framework = "laravel"
		r.BuildCommand = "composer install --no-dev --optimize-autoloader && php artisan config:cache && php artisan route:cache"
		r.StartCommand = "php artisan serve --host=0.0.0.0 --port=8080"
	case comp.Require["symfony/http-kernel"] != "" || comp.Require["symfony/framework-bundle"] != "":
		r.Framework = "symfony"
		r.BuildCommand = "composer install --no-dev --optimize-autoloader"
		r.StartCommand = "php -S 0.0.0.0:8080 -t public"
	case comp.Require["codeigniter4/framework"] != "":
		r.Framework = "codeigniter"
		r.BuildCommand = "composer install --no-dev --optimize-autoloader"
		r.StartCommand = "php spark serve --host=0.0.0.0 --port=8080"
	case comp.Require["cakephp/cakephp"] != "":
		r.Framework = "cakephp"
		r.BuildCommand = "composer install --no-dev"
		r.StartCommand = "php -S 0.0.0.0:8080 -t webroot"
	case comp.Require["slim/slim"] != "":
		r.Framework = "slim"
		r.BuildCommand = "composer install --no-dev"
		r.StartCommand = "php -S 0.0.0.0:8080 -t public"
	default:
		r.Framework = "php"
		r.BuildCommand = "composer install --no-dev"
		r.StartCommand = "php -S 0.0.0.0:8080 -t public"
	}

	return r
}

func detectPHPVersion(root string, require map[string]string) string {
	if v := require["php"]; v != "" {
		parts := strings.FieldsFunc(v, func(ch rune) bool {
			return ch == '>' || ch == '<' || ch == '=' || ch == '^' || ch == '~' || ch == ' ' || ch == '*' || ch == '|'
		})
		if len(parts) > 0 {
			return parts[0]
		}
	}
	if data, err := os.ReadFile(filepath.Join(root, ".php-version")); err == nil {
		v := strings.TrimSpace(string(data))
		if v != "" {
			return v
		}
	}
	return "8.2"
}

// ─── Static ──────────────────────────────────────────────────────────────────

func detectStatic(root string) *Result {
	if _, err := os.Stat(filepath.Join(root, "index.html")); err == nil {
		return &Result{
			Language:     "static",
			Framework:    "html",
			Version:      "",
			BuildCommand: "",
			StartCommand: "nginx -g 'daemon off;'",
			AppPort:      80,
			BaseImage:    "nginx:alpine",
		}
	}
	return nil
}
