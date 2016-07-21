package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/tcnksm/go-gitconfig"
)

//go:generate go-bindata _templates/...

const (
	defaultVCS    = "github.com"
	modelDir      = "models"
	controllerDir = "controllers"
	targetFile    = "main.go"
)

func usage() {
	fmt.Fprintf(os.Stderr, `Usage of %s:
	%s new <project name>
	%s gen
`, os.Args[0], os.Args[0], os.Args[0])
	os.Exit(1)
}

func main() {

	if len(os.Args) < 2 {
		usage()
	}

	cmd := os.Args[1]

	switch cmd {
	case "gen":
		curDir, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

		if !fileExists(filepath.Join(curDir, targetFile)) || !fileExists(filepath.Join(curDir, modelDir)) {
			fmt.Fprintf(os.Stderr, `%s is not project root. Please move.
`, curDir)
			os.Exit(1)
		}
		cmdGen(curDir)

	case "new":
		var (
			vcs      string
			username string
		)

		flag := flag.NewFlagSet(os.Args[0], flag.ExitOnError)

		flag.Usage = func() {
			fmt.Fprintf(os.Stderr, `Usage of %s:
	%s new <project name>

Options:
`, os.Args[0], os.Args[0])
			flag.PrintDefaults()
		}

		flag.StringVar(&vcs, "v", "", "VCS")
		flag.StringVar(&username, "u", "", "Username")

		if len(os.Args) < 3 {
			flag.Usage()
			os.Exit(1)
		}

		flag.Parse(os.Args[3:])

		if vcs == "" {
			vcs = defaultVCS
		}

		if username == "" {
			var err error
			username, err = gitconfig.GithubUser()

			if err != nil {
				username, err = gitconfig.Username()
				if err != nil {
					msg := "Cannot find `~/.gitconfig` file.\n" +
						"Please use -u option"
					fmt.Println(msg)
					os.Exit(1)
				}
			}
		}

		project := os.Args[2]

		detail := &Detail{VCS: vcs, User: username, Project: project}

		cmdNew(detail)

	default:
		usage()
	}

}

func cmdNew(detail *Detail) {
	gopath := os.Getenv("GOPATH")

	if gopath == "" {
		fmt.Println("Error: $GOPATH is not found")
		os.Exit(1)
	}

	outDir := filepath.Join(gopath, "src", detail.VCS, detail.User, detail.Project)

	if err := generateSkeleton(detail, outDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stdout, `===> Created %s
`, outDir)
}

func cmdGen(outDir string) {
	absModelDir := filepath.Join(outDir, modelDir)
	files, err := ioutil.ReadDir(absModelDir)

	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	var models []*Model
	mmap := make(map[string]*Model)

	for _, file := range files {
		if file.IsDir() {
			continue
		}

		if !strings.HasSuffix(file.Name(), ".go") {
			continue
		}

		modelPath := filepath.Join(absModelDir, file.Name())
		ms, err := parseModel(modelPath)

		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

		for _, model := range ms {
			models = append(models, model)
			mmap[model.Name] = model
		}
	}

	paths, err := parseMain(filepath.Join(outDir, targetFile))

	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	importDir := formatImportDir(paths)

	switch {
	case len(importDir) > 1:
		fmt.Println("Error: Conflict import path. Please check 'main.go'.")
		os.Exit(1)

	case len(importDir) == 0:
		fmt.Println("Error: Can't refer import path. Please check 'main.go'.")
		os.Exit(1)
	}

	vcs := filepath.Base(filepath.Dir(filepath.Dir(importDir[0])))
	user := filepath.Base(filepath.Dir(importDir[0]))
	project := filepath.Base(importDir[0])

	for _, model := range models {

		// Check association, stdout "model.Fields[0].Association.Type"
		resolveAssoc(model, mmap, make(map[string]bool))

		d := &Detail{
			Model:     model,
			ImportDir: importDir[0],
			VCS:       vcs,
			User:      user,
			Project:   project,
		}

		if err := generateApibModel(d, outDir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

		if err := generateController(d, outDir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	detail := &Detail{
		Models:    models,
		ImportDir: importDir[0],
		VCS:       vcs,
		User:      user,
		Project:   filepath.Base(importDir[0]),
	}

	if err := generateApibIndex(detail, outDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if err := generateRouter(detail, outDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if err := generateREADME(models, outDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Println("===> Generated...")
}

func formatImportDir(paths []string) []string {
	results := make([]string, 0, len(paths))
	flag := map[string]bool{}
	for i := 0; i < len(paths); i++ {
		dir := filepath.Dir(paths[i])
		if !flag[dir] {
			flag[dir] = true
			results = append(results, dir)
		}
	}
	return results
}

func resolveAssoc(model *Model, mmap map[string]*Model, parents map[string]bool) {
	parents[model.Name] = true

	for i, field := range model.Fields {
		str := strings.Trim(field.Type, "[]*")
		if mmap[str] != nil && parents[str] != true {
			resolveAssoc(mmap[str], mmap, parents)

			var assoc int
			switch string([]rune(field.Type)[0]) {
			case "[":
				if validateFKey(mmap[str].Fields, model.Name) {
					assoc = AssociationHasMany
					break
				}
				assoc = AssociationBelongsTo

			default:
				if validateFKey(mmap[str].Fields, model.Name) {
					assoc = AssociationHasOne
					break
				}
				assoc = AssociationBelongsTo
			}
			model.Fields[i].Association = &Association{Type: assoc, Model: mmap[str]}
		} else {
			model.Fields[i].Association = &Association{Type: AssociationNone}
		}
	}
}

func validateFKey(fields []*Field, name string) bool {
	for _, field := range fields {
		val := name + "ID"
		if field.Name == val {
			return true
		}
	}
	return false
}
