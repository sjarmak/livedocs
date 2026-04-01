package drift

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractPythonExports_Functions(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "api.py"), []byte(`
def public_func():
    """A public function."""
    pass

def _private_func():
    """A private function."""
    pass

def __dunder_init__(self):
    pass

async def async_handler(request):
    """An async public function."""
    pass
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	symbols, err := ExtractPythonExports(dir)
	if err != nil {
		t.Fatal(err)
	}

	symbolSet := make(map[string]bool)
	for _, s := range symbols {
		symbolSet[s] = true
	}

	if !symbolSet["public_func"] {
		t.Error("expected public_func in exports")
	}
	if !symbolSet["async_handler"] {
		t.Error("expected async_handler in exports")
	}
	if symbolSet["_private_func"] {
		t.Error("_private_func should be excluded (underscore prefix)")
	}
	if symbolSet["__dunder_init__"] {
		t.Error("__dunder_init__ should be excluded (dunder)")
	}
}

func TestExtractPythonExports_Classes(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "models.py"), []byte(`
class UserModel:
    """A public class."""
    def method(self):
        pass

class _InternalModel:
    """An internal class."""
    pass
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	symbols, err := ExtractPythonExports(dir)
	if err != nil {
		t.Fatal(err)
	}

	symbolSet := make(map[string]bool)
	for _, s := range symbols {
		symbolSet[s] = true
	}

	if !symbolSet["UserModel"] {
		t.Error("expected UserModel in exports")
	}
	if symbolSet["_InternalModel"] {
		t.Error("_InternalModel should be excluded (underscore prefix)")
	}
	// Methods should not appear as top-level exports.
	if symbolSet["method"] {
		t.Error("method should not be a top-level export")
	}
}

func TestExtractPythonExports_ModuleLevelVars(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "config.py"), []byte(`
MAX_RETRIES = 3
DEFAULT_TIMEOUT = 30
_internal_cache = {}

class Config:
    pass
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	symbols, err := ExtractPythonExports(dir)
	if err != nil {
		t.Fatal(err)
	}

	symbolSet := make(map[string]bool)
	for _, s := range symbols {
		symbolSet[s] = true
	}

	if !symbolSet["MAX_RETRIES"] {
		t.Error("expected MAX_RETRIES in exports")
	}
	if !symbolSet["DEFAULT_TIMEOUT"] {
		t.Error("expected DEFAULT_TIMEOUT in exports")
	}
	if symbolSet["_internal_cache"] {
		t.Error("_internal_cache should be excluded (underscore prefix)")
	}
	if !symbolSet["Config"] {
		t.Error("expected Config in exports")
	}
}

func TestExtractPythonExports_Decorators(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "views.py"), []byte(`
from dataclasses import dataclass

@dataclass
class Point:
    x: float
    y: float

@app.route("/api")
def api_endpoint():
    pass

@staticmethod
def helper():
    pass
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	symbols, err := ExtractPythonExports(dir)
	if err != nil {
		t.Fatal(err)
	}

	symbolSet := make(map[string]bool)
	for _, s := range symbols {
		symbolSet[s] = true
	}

	if !symbolSet["Point"] {
		t.Error("expected decorated class Point in exports")
	}
	if !symbolSet["api_endpoint"] {
		t.Error("expected decorated function api_endpoint in exports")
	}
}

func TestExtractPythonExports_SkipsTestFiles(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "app.py"), []byte(`
def main():
    pass
`), 0644)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(dir, "test_app.py"), []byte(`
def test_main():
    pass

class TestApp:
    pass
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	symbols, err := ExtractPythonExports(dir)
	if err != nil {
		t.Fatal(err)
	}

	symbolSet := make(map[string]bool)
	for _, s := range symbols {
		symbolSet[s] = true
	}

	if !symbolSet["main"] {
		t.Error("expected main from app.py")
	}
	if symbolSet["test_main"] {
		t.Error("test_main from test file should be excluded")
	}
	if symbolSet["TestApp"] {
		t.Error("TestApp from test file should be excluded")
	}
}

func TestExtractPythonExports_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	symbols, err := ExtractPythonExports(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(symbols) != 0 {
		t.Errorf("expected no symbols from empty dir, got %v", symbols)
	}
}

func TestExtractTypeScriptExports_Functions(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "api.ts"), []byte(`
export function createClient(config: Config): Client {
    return new Client(config);
}

function internalHelper(): void {
    // not exported
}

export async function fetchData(url: string): Promise<Data> {
    return fetch(url).then(r => r.json());
}
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	symbols, err := ExtractTypeScriptExports(dir)
	if err != nil {
		t.Fatal(err)
	}

	symbolSet := make(map[string]bool)
	for _, s := range symbols {
		symbolSet[s] = true
	}

	if !symbolSet["createClient"] {
		t.Error("expected createClient in exports")
	}
	if !symbolSet["fetchData"] {
		t.Error("expected fetchData in exports")
	}
	if symbolSet["internalHelper"] {
		t.Error("internalHelper should not be in exports (not exported)")
	}
}

func TestExtractTypeScriptExports_ClassesAndInterfaces(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "types.ts"), []byte(`
export class UserService {
    constructor(private repo: UserRepo) {}
    async getUser(id: string): Promise<User> {
        return this.repo.find(id);
    }
}

export interface Config {
    apiUrl: string;
    timeout: number;
}

export type UserId = string;

export enum Status {
    Active = "active",
    Inactive = "inactive",
}

class InternalCache {
    // not exported
}

interface PrivateConfig {
    // not exported
}
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	symbols, err := ExtractTypeScriptExports(dir)
	if err != nil {
		t.Fatal(err)
	}

	symbolSet := make(map[string]bool)
	for _, s := range symbols {
		symbolSet[s] = true
	}

	if !symbolSet["UserService"] {
		t.Error("expected UserService in exports")
	}
	if !symbolSet["Config"] {
		t.Error("expected Config in exports")
	}
	if !symbolSet["UserId"] {
		t.Error("expected UserId in exports")
	}
	if !symbolSet["Status"] {
		t.Error("expected Status in exports")
	}
	if symbolSet["InternalCache"] {
		t.Error("InternalCache should not be in exports (not exported)")
	}
	if symbolSet["PrivateConfig"] {
		t.Error("PrivateConfig should not be in exports (not exported)")
	}
}

func TestExtractTypeScriptExports_ExportDefault(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "main.ts"), []byte(`
export default class App {
    start(): void {}
}

export const VERSION = "1.0.0";
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	symbols, err := ExtractTypeScriptExports(dir)
	if err != nil {
		t.Fatal(err)
	}

	symbolSet := make(map[string]bool)
	for _, s := range symbols {
		symbolSet[s] = true
	}

	if !symbolSet["App"] {
		t.Error("expected default exported class App in exports")
	}
	if !symbolSet["VERSION"] {
		t.Error("expected VERSION in exports")
	}
}

func TestExtractTypeScriptExports_SkipsTestFiles(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "service.ts"), []byte(`
export function serve(): void {}
`), 0644)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(dir, "service.test.ts"), []byte(`
export function testServe(): void {}
`), 0644)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(dir, "service.spec.ts"), []byte(`
export function specServe(): void {}
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	symbols, err := ExtractTypeScriptExports(dir)
	if err != nil {
		t.Fatal(err)
	}

	symbolSet := make(map[string]bool)
	for _, s := range symbols {
		symbolSet[s] = true
	}

	if !symbolSet["serve"] {
		t.Error("expected serve from service.ts")
	}
	if symbolSet["testServe"] {
		t.Error("testServe from test file should be excluded")
	}
	if symbolSet["specServe"] {
		t.Error("specServe from spec file should be excluded")
	}
}

func TestExtractTypeScriptExports_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	symbols, err := ExtractTypeScriptExports(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(symbols) != 0 {
		t.Errorf("expected no symbols from empty dir, got %v", symbols)
	}
}

func TestExtractCodeExports_MixedLanguages(t *testing.T) {
	dir := t.TempDir()

	// Go file
	err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(`package main
func Exported() {}
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Python file
	err = os.WriteFile(filepath.Join(dir, "helper.py"), []byte(`
def helper_func():
    pass
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// TypeScript file
	err = os.WriteFile(filepath.Join(dir, "utils.ts"), []byte(`
export function tsHelper(): void {}
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	symbols, err := ExtractCodeExports(dir)
	if err != nil {
		t.Fatal(err)
	}

	symbolSet := make(map[string]bool)
	for _, s := range symbols {
		symbolSet[s] = true
	}

	if !symbolSet["Exported"] {
		t.Error("expected Go export Exported")
	}
	if !symbolSet["helper_func"] {
		t.Error("expected Python export helper_func")
	}
	if !symbolSet["tsHelper"] {
		t.Error("expected TypeScript export tsHelper")
	}
}

func TestExtractCodeExports_PythonOnly(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "app.py"), []byte(`
class Application:
    pass

def run():
    pass
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	symbols, err := ExtractCodeExports(dir)
	if err != nil {
		t.Fatal(err)
	}

	symbolSet := make(map[string]bool)
	for _, s := range symbols {
		symbolSet[s] = true
	}

	if !symbolSet["Application"] {
		t.Error("expected Application")
	}
	if !symbolSet["run"] {
		t.Error("expected run")
	}
}

func TestDetect_PythonSymbols(t *testing.T) {
	dir := t.TempDir()

	// Write a Python source file.
	err := os.WriteFile(filepath.Join(dir, "api.py"), []byte(`
class UserService:
    pass

def create_user():
    pass

def delete_user():
    pass
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Write a README referencing one real and one stale symbol.
	readmePath := filepath.Join(dir, "README.md")
	err = os.WriteFile(readmePath, []byte("Use `create_user` to register. The `UserService` class handles users. Also see `removed_endpoint` for legacy.\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	report, err := Detect(readmePath, "")
	if err != nil {
		t.Fatal(err)
	}

	// removed_endpoint should be stale.
	foundStale := false
	for _, f := range report.Findings {
		if f.Kind == StaleReference && f.Symbol == "removed_endpoint" {
			foundStale = true
		}
	}
	if !foundStale {
		t.Error("removed_endpoint should be a stale reference")
	}

	// delete_user should be undocumented.
	foundUndoc := false
	for _, f := range report.Findings {
		if f.Kind == Undocumented && f.Symbol == "delete_user" {
			foundUndoc = true
		}
	}
	if !foundUndoc {
		t.Error("delete_user should be undocumented")
	}
}

func TestDetect_TypeScriptSymbols(t *testing.T) {
	dir := t.TempDir()

	// Write a TypeScript source file.
	err := os.WriteFile(filepath.Join(dir, "api.ts"), []byte(`
export function createClient(): void {}

export interface ApiConfig {
    url: string;
}

export function deleteClient(): void {}
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Write a README referencing one real and one stale symbol.
	readmePath := filepath.Join(dir, "README.md")
	err = os.WriteFile(readmePath, []byte("Use `createClient` to connect. Configure via `ApiConfig`. Also see `oldMethod` for legacy.\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	report, err := Detect(readmePath, "")
	if err != nil {
		t.Fatal(err)
	}

	// oldMethod should be stale.
	foundStale := false
	for _, f := range report.Findings {
		if f.Kind == StaleReference && f.Symbol == "oldMethod" {
			foundStale = true
		}
	}
	if !foundStale {
		t.Error("oldMethod should be a stale reference")
	}

	// deleteClient should be undocumented.
	foundUndoc := false
	for _, f := range report.Findings {
		if f.Kind == Undocumented && f.Symbol == "deleteClient" {
			foundUndoc = true
		}
	}
	if !foundUndoc {
		t.Error("deleteClient should be undocumented")
	}
}
