package build

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"plenti/readers"
	"regexp"
	"strconv"
	"strings"
	"time"

	"rogchap.com/v8go"
)

// SSRctx is a v8go context for loaded with components needed to render HTML.
var SSRctx *v8go.Context

// Client builds the SPA.
func Client(buildPath string, tempBuildDir string, ejectedPath string) {

	defer Benchmark(time.Now(), "Compiling client SPA with Svelte")

	Log("\nCompiling client SPA with svelte")

	stylePath := buildPath + "/spa/bundle.css"

	// Initialize string for layout.js component list.
	var allComponentsStr string

	// Set up counter for logging output.
	compiledComponentCounter := 0

	// Get svelte compiler code from node_modules.
	compiler, err := ioutil.ReadFile(tempBuildDir + "node_modules/svelte/compiler.js")
	if err != nil {
		fmt.Printf("Can't read %vnode_modules/svelte/compiler.js: %v", tempBuildDir, err)
	}
	// Remove reference to 'self' that breaks v8go on line 19 of node_modules/svelte/compiler.js.
	compilerStr := strings.Replace(string(compiler), "self.performance.now();", "'';", 1)
	ctx, _ := v8go.NewContext(nil)
	_, addCompilerErr := ctx.RunScript(compilerStr, "compile_svelte")
	if addCompilerErr != nil {
		fmt.Printf("Could not add svelte compiler: %v\n", addCompilerErr)
	}

	SSRctx, _ = v8go.NewContext(nil)
	// Fix "ReferenceError: exports is not defined" errors on line 1319 (exports.current_component;).
	SSRctx.RunScript("var exports = {};", "create_ssr")
	// Fix "TypeError: Cannot read property 'noop' of undefined" from node_modules/svelte/store/index.js.
	SSRctx.RunScript("function noop(){}", "create_ssr")

	var svelteLibs = [6]string{
		tempBuildDir + "node_modules/svelte/animate/index.js",
		tempBuildDir + "node_modules/svelte/easing/index.js",
		tempBuildDir + "node_modules/svelte/internal/index.js",
		tempBuildDir + "node_modules/svelte/motion/index.js",
		tempBuildDir + "node_modules/svelte/store/index.js",
		tempBuildDir + "node_modules/svelte/transition/index.js",
	}

	for _, svelteLib := range svelteLibs {
		// Use v8go and add create_ssr_component() function.
		createSsrComponent, npmReadErr := ioutil.ReadFile(svelteLib)
		if npmReadErr != nil {
			fmt.Printf("Can't read %v: %v", svelteLib, npmReadErr)
		}
		// Fix "Cannot access 'on_destroy' before initialization" errors on line 1320 & line 1337 of node_modules/svelte/internal/index.js.
		createSsrStr := strings.ReplaceAll(string(createSsrComponent), "function create_ssr_component(fn) {", "function create_ssr_component(fn) {var on_destroy= {};")
		// Use empty noop() function created above instead of missing method.
		createSsrStr = strings.ReplaceAll(createSsrStr, "internal.noop", "noop")
		_, createFuncErr := SSRctx.RunScript(createSsrStr, "create_ssr")
		if err != nil {
			fmt.Printf("Could not add create_ssr_component() func from svelte/internal: %v", createFuncErr)
		}
	}

	// Compile router separately since it's ejected from core.
	compileSvelte(ctx, SSRctx, ejectedPath+"/router.svelte", buildPath+"/spa/ejected/router.js", stylePath, tempBuildDir)

	// Go through all file paths in the "/layout" folder.
	layoutFilesErr := filepath.Walk(tempBuildDir+"layout", func(layoutPath string, layoutFileInfo os.FileInfo, err error) error {
		// Create destination path.
		destFile := buildPath + "/spa" + strings.TrimPrefix(layoutPath, tempBuildDir+"layout")
		// Make sure path is a directory
		if layoutFileInfo.IsDir() {
			// Create any sub directories need for filepath.
			os.MkdirAll(destFile, os.ModePerm)
		} else {
			// If the file is in .svelte format, compile it to .js
			if filepath.Ext(layoutPath) == ".svelte" {

				// Replace .svelte file extension with .js.
				destFile = strings.TrimSuffix(destFile, filepath.Ext(destFile)) + ".js"

				compileSvelte(ctx, SSRctx, layoutPath, destFile, stylePath, tempBuildDir)

				// Remove temporary theme build directory.
				destLayoutPath := strings.TrimPrefix(layoutPath, tempBuildDir)
				// Create entry for layout.js.
				layoutSignature := strings.ReplaceAll(strings.ReplaceAll((destLayoutPath), "/", "_"), ".", "_")
				// Remove layout directory.
				destLayoutPath = strings.TrimPrefix(destLayoutPath, "layout/")
				// Compose entry for layout.js file.
				allComponentsStr = allComponentsStr + "export {default as " + layoutSignature + "} from '../" + destLayoutPath + "';\n"

				compiledComponentCounter++

			}
		}
		return nil
	})
	if layoutFilesErr != nil {
		fmt.Printf("Could not get layout file: %s", layoutFilesErr)
	}

	// Write layout.js to filesystem.
	compWriteErr := ioutil.WriteFile(buildPath+"/spa/ejected/layout.js", []byte(allComponentsStr), os.ModePerm)
	if compWriteErr != nil {
		fmt.Printf("Unable to write layout.js file: %v\n", compWriteErr)
	}

	Log("Number of components compiled: " + strconv.Itoa(compiledComponentCounter))
}

func compileSvelte(ctx *v8go.Context, SSRctx *v8go.Context, layoutPath string, destFile string, stylePath string, tempBuildDir string) {

	component, err := ioutil.ReadFile(layoutPath)
	if err != nil {
		fmt.Printf("Can't read component: %v\n", err)
	}
	componentStr := string(component)

	// Compile component with Svelte.
	ctx.RunScript("var { js, css } = svelte.compile(`"+componentStr+"`, {css: false, hydratable: true});", "compile_svelte")

	// Get the JS code from the compiled result.
	jsCode, err := ctx.RunScript("js.code;", "compile_svelte")
	if err != nil {
		fmt.Printf("V8go could not execute js.code: %v", err)
	}
	jsBytes := []byte(jsCode.String())
	jsWriteErr := ioutil.WriteFile(destFile, jsBytes, 0755)
	if jsWriteErr != nil {
		fmt.Printf("Unable to write compiled client file: %v\n", jsWriteErr)
	}

	// Get the CSS code from the compiled result.
	cssCode, err := ctx.RunScript("css.code;", "compile_svelte")
	if err != nil {
		fmt.Printf("V8go could not execute css.code: %v", err)
	}
	cssStr := strings.TrimSpace(cssCode.String())
	// If there is CSS, write it into the bundle.css file.
	if cssStr != "null" {
		cssFile, WriteStyleErr := os.OpenFile(stylePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if WriteStyleErr != nil {
			fmt.Printf("Could not open bundle.css for writing: %s", WriteStyleErr)
		}
		defer cssFile.Close()
		if _, err := cssFile.WriteString(cssStr); err != nil {
			log.Println(err)
		}
	}

	// Get Server Side Rendered (SSR) JS.
	_, ssrCompileErr := ctx.RunScript("var { js: ssrJs, css: ssrCss } = svelte.compile(`"+componentStr+"`, {generate: 'ssr'});", "compile_svelte")
	if ssrCompileErr != nil {
		fmt.Printf("V8go could not compile ssrJs.code: %v\n", ssrCompileErr)
	}
	ssrJsCode, err := ctx.RunScript("ssrJs.code;", "compile_svelte")
	if err != nil {
		fmt.Printf("V8go could not get ssrJs.code value: %v\n", err)
	}
	// Regex match static import statements.
	reStaticImport := regexp.MustCompile(`import\s((.*)\sfrom(.*);|(((.*)\n){0,})\}\sfrom(.*);)`)
	// Regex match static export statements.
	reStaticExport := regexp.MustCompile(`export\s(.*);`)
	// Remove static import statements.
	ssrStr := reStaticImport.ReplaceAllString(ssrJsCode.String(), `/*$0*/`)
	// Remove static export statements.
	ssrStr = reStaticExport.ReplaceAllString(ssrStr, `/*$0*/`)
	// Use var instead of const so it can be redeclared multiple times.
	reConst := regexp.MustCompile(`(?m)^const\s`)
	ssrStr = reConst.ReplaceAllString(ssrStr, "var ")
	// Remove temporary theme directory info from path before making a comp signature.
	layoutPath = strings.TrimPrefix(layoutPath, tempBuildDir)
	// Create custom variable name for component based on the file path for the layout.
	componentSignature := strings.ReplaceAll(strings.ReplaceAll(layoutPath, "/", "_"), ".", "_")
	// Use signature instead of generic "Component". Add space to avoid also replacing part of "loadComponent".
	ssrStr = strings.ReplaceAll(ssrStr, " Component ", " "+componentSignature+" ")

	namedExports := reStaticExport.FindAllStringSubmatch(ssrStr, -1)
	// Loop through all export statements.
	for _, namedExport := range namedExports {
		// Get exported functions that aren't default.
		// Ignore names that contain semicolons to try to avoid pulling in CSS map code: https://github.com/sveltejs/svelte/issues/3604
		if !strings.HasPrefix(namedExport[1], "default ") && !strings.Contains(namedExport[1], ";") {
			// Get just the name(s) inside the curly brackets
			exportNames := makeNameList(namedExport)
			for _, exportName := range exportNames {
				if exportName != "" && componentSignature != "" {
					// Create new component signature with variable name appended to the end.
					ssrStr = strings.ReplaceAll(ssrStr, exportName, componentSignature+"_"+exportName)
				}
			}
		}
	}

	// Replace import references with variable signatures.
	reStaticImportPath := regexp.MustCompile(`(?:'|").*(?:'|")`)
	reStaticImportName := regexp.MustCompile(`import\s(.*)\sfrom`)
	namedImports := reStaticImport.FindAllString(ssrStr, -1)
	for _, namedImport := range namedImports {
		// Get path only from static import statement.
		importPath := reStaticImportPath.FindString(namedImport)
		importNameSlice := reStaticImportName.FindStringSubmatch(namedImport)
		importNameStr := ""
		var namedImportNameStrs []string
		if len(importNameSlice) > 0 {
			importNameStr = importNameSlice[1]
			// Check if it's a named import (starts w curly bracket)
			// and import path should not have spaces (ignores CSS mapping: https://github.com/sveltejs/svelte/issues/3604).
			if strings.Contains(importNameSlice[1], "{") && !strings.Contains(importPath, " ") {
				namedImportNameStrs = makeNameList(importNameSlice)
			}
		}
		// Remove quotes around path.
		importPath = strings.Trim(importPath, `'"`)
		// Get individual path arguments.
		layoutParts := strings.Split(layoutPath, "/")
		layoutFileName := layoutParts[len(layoutParts)-1]
		layoutRootPath := strings.TrimSuffix(layoutPath, layoutFileName)

		importParts := strings.Split(importPath, "/")
		// Initialize the import signature that will be used for unique variable names.
		importSignature := ""
		// Check if the path ends with a file extension, e.g. ".svelte".
		if len(filepath.Ext(importParts[len(importParts)-1])) > 1 {
			for _, importPart := range importParts {
				// Check if path starts relative to current folder.
				if importPart == "." {
					// Remove the proceeding dot so the file can be combined with the root.
					importPath = strings.TrimPrefix(importPath, "./")
				}
				// Check if path goes up a folder.
				if importPart == ".." {
					// Remove the proceeding double dots so it can be combined with root.
					importPath = strings.TrimPrefix(importPath, importPart+"/")
					// Split the layout root path so we can remove the last segment since the double dots indicates going back a folder.
					layoutParts = strings.Split(layoutRootPath, "/")
					layoutRootPath = strings.TrimSuffix(layoutRootPath, layoutParts[len(layoutParts)-2]+"/")
				}
			}
			// Create the variable name from the full path.
			importSignature = strings.ReplaceAll(strings.ReplaceAll((layoutRootPath+importPath), "/", "_"), ".", "_")
		}
		// TODO: Add an else ^ to account for NPM dependencies?

		// Check that there is a valid import to replace.
		if importNameStr != "" && importSignature != "" {
			// Only use comp signatures inside JS template literal placeholders.
			reTemplatePlaceholder := regexp.MustCompile(`(?s)\$\{validate_component\(.*\)\}`)
			// Only replace this specific variable, so not anything that has letters, underscores, or numbers attached to it.
			reImportNameUse := regexp.MustCompile(`([^a-zA-Z_0-9])` + importNameStr + `([^a-zA-Z_0-9])`)
			// Find the template placeholders.
			ssrStr = reTemplatePlaceholder.ReplaceAllStringFunc(ssrStr,
				func(placeholder string) string {
					// Use the signature instead of variable name.
					return reImportNameUse.ReplaceAllString(placeholder, "${1}"+importSignature+"${2}")
				},
			)
		}

		// Handle each named import, e.g. import { first, second } from "./whatever.svelte".
		for _, currentNamedImport := range namedImportNameStrs {
			// Remove whitespace on sides that might occur when splitting into array by comma.
			currentNamedImport = strings.TrimSpace(currentNamedImport)
			// Check that there is a valid named import.
			if currentNamedImport != "" && importSignature != "" {
				// Only add named imports to create_ssr_component().
				reCreateFunc := regexp.MustCompile(`(create_ssr_component\(\(.*\)\s=>\s\{)`)
				// Entry should be block scoped, like: let count = layout_scripts_stores_svelte_count;
				blockScopedVar := "\n let " + currentNamedImport + " = " + importSignature + "_" + currentNamedImport + ";"
				// Add block scoped var inside create_ssr_component.
				ssrStr = reCreateFunc.ReplaceAllString(ssrStr, "${1}"+blockScopedVar)
			}
		}
	}

	// Remove allComponents object (leaving just componentSignature) for SSR.
	// Match: allComponents.layout_components_grid_svelte
	reAllComponentsDot := regexp.MustCompile(`allComponents\.(layout_.*_svelte)`)
	ssrStr = reAllComponentsDot.ReplaceAllString(ssrStr, "${1}")
	// Match: allComponents[component]
	reAllComponentsBracket := regexp.MustCompile(`allComponents\[(.*)\]`)
	ssrStr = reAllComponentsBracket.ReplaceAllString(ssrStr, "globalThis[${1}]")
	// Match: allComponents["layout_components_decrementer_svelte"]
	reAllComponentsBracketStr := regexp.MustCompile(`allComponents\[\"(.*)\"\]`)
	ssrStr = reAllComponentsBracketStr.ReplaceAllString(ssrStr, "${1}")

	paginatedContent, _ := getPagination()
	for _, pager := range paginatedContent {
		if "layout_content_"+pager.contentType+"_svelte" == componentSignature {
			for _, paginationVar := range pager.paginationVars {
				// Prefix var so it doesn't conflict with other variables.
				globalVar := "plenti_global_pager_" + paginationVar
				// Initialize var outside of function to set it as global.
				ssrStr = "var " + globalVar + ";\n" + ssrStr
				// Match where the pager var is set, like: let totalPages = Math.ceil(totalPosts / postsPerPage);
				reLocalVar := regexp.MustCompile(`((let\s|const\s|var\s)` + paginationVar + `.*;)`)
				// Create statement to assign local var to global var.
				makeGlobalVar := globalVar + " = " + paginationVar + ";"
				// Assign value to global var inside create_ssr_component() func, like: plenti_global_pager_totalPages = totalPages;
				ssrStr = reLocalVar.ReplaceAllString(ssrStr, "${1}\n"+makeGlobalVar)
				// Clear out styles for SSR since they are already pulled from client components.
				ssrStr = removeCSS(ssrStr)
			}
		}
	}

	// Add component to context so it can be used to render HTML in data_source.go.
	_, addSSRCompErr := SSRctx.RunScript(ssrStr, "create_ssr")
	if addSSRCompErr != nil {
		fmt.Printf("Could not add SSR Component: %v\n", addSSRCompErr)
	}

}

func removeCSS(str string) string {
	// Match var css = { ... }
	reCSS := regexp.MustCompile(`var(\s)css(\s)=(\s)\{(.*\n){0,}\};`)
	// Delete these styles because they often break pagination SSR.
	return reCSS.ReplaceAllString(str, "")
}

func makeNameList(importNameSlice []string) []string {
	var namedImportNameStrs []string
	// Get just the name(s) of the variable(s).
	namedImportNameStr := strings.Trim(importNameSlice[1], "{ }")
	// Chech if there are multiple names separated by a comma.
	if strings.Contains(namedImportNameStr, ",") {
		// Break apart by comma and add to individual items to array.
		namedImportNameStrs = append(namedImportNameStrs, strings.Split(namedImportNameStr, ",")...)
		for i := range namedImportNameStrs {
			// Remove surrounding whitespace (this will be present if there was space after the comma).
			namedImportNameStrs[i] = strings.TrimSpace(namedImportNameStrs[i])
		}
	} else {
		// Only one name was used, so add it directly to the array.
		namedImportNameStrs = append(namedImportNameStrs, namedImportNameStr)
	}
	return namedImportNameStrs
}

type pager struct {
	contentType    string
	contentPath    string
	paginationVars []string
}

func getPagination() ([]pager, *regexp.Regexp) {
	// Get settings from config file.
	siteConfig, _ := readers.GetSiteConfig(".")
	// Setup regex to find pagination.
	rePaginate := regexp.MustCompile(`:paginate\((.*?)\)`)
	// Initialize new pager struct
	var pagers []pager
	// Check for pagination in plenti.json config file.
	for configContentType, slug := range siteConfig.Types {
		// Initialize list of all :paginate() vars in a given slug.
		replacements := []string{}
		// Find every instance of :paginate() in the slug.
		paginateReplacements := rePaginate.FindAllStringSubmatch(slug, -1)
		// Loop through all :paginate() replacements found in config file.
		for _, replacement := range paginateReplacements {
			// Add the variable name defined within the brackets to the list.
			replacements = append(replacements, replacement[1])
		}
		var pager pager
		pager.contentType = configContentType
		pager.contentPath = slug
		pager.paginationVars = replacements
		pagers = append(pagers, pager)
	}
	return pagers, rePaginate
}
