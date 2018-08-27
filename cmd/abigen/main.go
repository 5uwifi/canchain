package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"strings"

	"github.com/5uwifi/canchain/accounts/abi/bind"
	"github.com/5uwifi/canchain/common/compiler"
)

var (
	abiFlag = flag.String("abi", "", "Path to the CANChain contract ABI json to bind, - for STDIN")
	binFlag = flag.String("bin", "", "Path to the CANChain contract bytecode (generate deploy method)")
	typFlag = flag.String("type", "", "Struct name for the binding (default = package name)")

	solFlag  = flag.String("sol", "", "Path to the CANChain contract Solidity source to build and bind")
	solcFlag = flag.String("solc", "solc", "Solidity compiler to use if source builds are requested")
	excFlag  = flag.String("exc", "", "Comma separated types to exclude from binding")

	pkgFlag  = flag.String("pkg", "", "Package name to generate the binding into")
	outFlag  = flag.String("out", "", "Output file for the generated binding (default = stdout)")
	langFlag = flag.String("lang", "go", "Destination language for the bindings (go, java, objc)")
)

func main() {
	flag.Parse()

	if *abiFlag == "" && *solFlag == "" {
		fmt.Printf("No contract ABI (--abi) or Solidity source (--sol) specified\n")
		os.Exit(-1)
	} else if (*abiFlag != "" || *binFlag != "" || *typFlag != "") && *solFlag != "" {
		fmt.Printf("Contract ABI (--abi), bytecode (--bin) and type (--type) flags are mutually exclusive with the Solidity source (--sol) flag\n")
		os.Exit(-1)
	}
	if *pkgFlag == "" {
		fmt.Printf("No destination package specified (--pkg)\n")
		os.Exit(-1)
	}
	var lang bind.Lang
	switch *langFlag {
	case "go":
		lang = bind.LangGo
	case "java":
		lang = bind.LangJava
	case "objc":
		lang = bind.LangObjC
	default:
		fmt.Printf("Unsupported destination language \"%s\" (--lang)\n", *langFlag)
		os.Exit(-1)
	}
	var (
		abis  []string
		bins  []string
		types []string
	)
	if *solFlag != "" || *abiFlag == "-" {
		exclude := make(map[string]bool)
		for _, kind := range strings.Split(*excFlag, ",") {
			exclude[strings.ToLower(kind)] = true
		}

		var contracts map[string]*compiler.Contract
		var err error
		if *solFlag != "" {
			contracts, err = compiler.CompileSolidity(*solcFlag, *solFlag)
			if err != nil {
				fmt.Printf("Failed to build Solidity contract: %v\n", err)
				os.Exit(-1)
			}
		} else {
			contracts, err = contractsFromStdin()
			if err != nil {
				fmt.Printf("Failed to read input ABIs from STDIN: %v\n", err)
				os.Exit(-1)
			}
		}
		for name, contract := range contracts {
			if exclude[strings.ToLower(name)] {
				continue
			}
			abi, _ := json.Marshal(contract.Info.AbiDefinition)
			abis = append(abis, string(abi))
			bins = append(bins, contract.Code)

			nameParts := strings.Split(name, ":")
			types = append(types, nameParts[len(nameParts)-1])
		}
	} else {
		abi, err := ioutil.ReadFile(*abiFlag)
		if err != nil {
			fmt.Printf("Failed to read input ABI: %v\n", err)
			os.Exit(-1)
		}
		abis = append(abis, string(abi))

		bin := []byte{}
		if *binFlag != "" {
			if bin, err = ioutil.ReadFile(*binFlag); err != nil {
				fmt.Printf("Failed to read input bytecode: %v\n", err)
				os.Exit(-1)
			}
		}
		bins = append(bins, string(bin))

		kind := *typFlag
		if kind == "" {
			kind = *pkgFlag
		}
		types = append(types, kind)
	}
	code, err := bind.Bind(types, abis, bins, *pkgFlag, lang)
	if err != nil {
		fmt.Printf("Failed to generate ABI binding: %v\n", err)
		os.Exit(-1)
	}
	if *outFlag == "" {
		fmt.Printf("%s\n", code)
		return
	}
	if err := ioutil.WriteFile(*outFlag, []byte(code), 0600); err != nil {
		fmt.Printf("Failed to write ABI binding: %v\n", err)
		os.Exit(-1)
	}
}

func contractsFromStdin() (map[string]*compiler.Contract, error) {
	bytes, err := ioutil.ReadAll(os.Stdin)
	if err != nil {
		return nil, err
	}

	return compiler.ParseCombinedJSON(bytes, "", "", "", "")
}
