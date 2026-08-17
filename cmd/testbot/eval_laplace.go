package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// eval-laplace runs a private, one-shot, blinded model comparison over a
// manifest of captured Laplace requests. It calls OpenRouter directly and
// never invokes application tools; synthesis lanes consume only tool results
// already frozen in the captured conversation.
var evalLaplaceCmd = &cobra.Command{
	Use:         "eval-laplace",
	Short:       "Run a blinded batch eval over captured Laplace traces",
	Annotations: map[string]string{"skip-bot-setup": "true"},
	RunE:        runEvalLaplace,
}

func init() {
	f := evalLaplaceCmd.Flags()
	f.String("manifest", "", "Private JSON manifest describing traces and replay lanes")
	f.String("variants-file", "", "JSON model variants with strict provider/privacy routing")
	f.StringSlice("variant", nil, "Two or more variant names to compare")
	f.String("out", "", "New private output directory (must not already exist)")
	f.String("blind-out", "", "New operator-only bundle index root (never give this root or index to a judge)")
	f.Duration("request-timeout", 10*time.Minute, "Timeout for one model generation")
	rootCmd.AddCommand(evalLaplaceCmd)
}

type laplaceEvalManifest struct {
	SchemaVersion int               `json:"schema_version"`
	FilesDir      string            `json:"files_dir"`
	Cases         []laplaceEvalCase `json:"cases"`
}

type laplaceEvalCase struct {
	ID        string            `json:"id"`
	TraceFile string            `json:"trace_file"`
	Agent     string            `json:"agent"`
	Category  string            `json:"category"`
	Subtype   string            `json:"subtype"`
	UserSlot  string            `json:"user_slot,omitempty"`
	Lanes     []laplaceEvalLane `json:"lanes"`
}

type laplaceEvalLane struct {
	Name                string `json:"name"`
	Gen                 string `json:"gen"`
	Kind                string `json:"kind,omitempty"`
	ForceToolChoiceNone bool   `json:"force_tool_choice_none"`
}

type laplaceEvalResult struct {
	CaseID   string             `json:"case_id"`
	Category string             `json:"category"`
	Subtype  string             `json:"subtype"`
	UserSlot string             `json:"user_slot,omitempty"`
	Lane     string             `json:"lane"`
	Replay   *replayRunResult   `json:"replay,omitempty"`
	ToolLoop *toolLoopRunResult `json:"tool_loop,omitempty"`
}

type toolLoopRunResult struct {
	Turns           []toolLoopTurnResult `json:"turns"`
	DecisionMatches int                  `json:"decision_matches"`
	ExactMatches    int                  `json:"exact_matches"`
	MalformedTurns  int                  `json:"malformed_turns"`
}

type toolLoopTurnResult struct {
	Turn       int                `json:"turn"`
	Replay     replayRunResult    `json:"replay"`
	Assessment toolTurnAssessment `json:"assessment"`
}

type blindEvalManifest struct {
	SchemaVersion int             `json:"schema_version"`
	MediaDir      string          `json:"media_dir,omitempty"`
	Cases         []blindEvalCase `json:"cases"`
}

// blindEvalIndex is operator-only routing metadata. Judges receive exactly one
// bundle directory from this index, never the index/root itself. Each bundle
// contains one decision unit, so a later teacher-forced request cannot reveal
// the captured answer to an earlier unit in the same judge packet.
type blindEvalIndex struct {
	SchemaVersion int      `json:"schema_version"`
	Bundles       []string `json:"bundles"`
}

type blindEvalCase struct {
	CaseID      string               `json:"case_id"`
	Category    string               `json:"category"`
	Subtype     string               `json:"subtype"`
	Lane        string               `json:"lane"`
	RequestFile string               `json:"request_file"`
	Candidates  []blindEvalCandidate `json:"candidates"`
}

type blindToolCandidatePacket struct {
	Kind           string                   `json:"kind"`
	Turns          []blindToolCandidateTurn `json:"turns"`
	MalformedTurns int                      `json:"malformed_turns"`
}

type blindToolCandidateTurn struct {
	Turn           int                       `json:"turn"`
	Status         string                    `json:"status"`
	Content        string                    `json:"content,omitempty"`
	ActualDecision string                    `json:"actual_decision"`
	ActualCalls    []blindToolCallAssessment `json:"actual_calls,omitempty"`
}

type blindToolCallAssessment struct {
	Call                 blindToolCall `json:"call"`
	KnownTool            bool          `json:"known_tool"`
	ArgumentsJSONValid   bool          `json:"arguments_json_valid"`
	ArgumentsSchemaValid bool          `json:"arguments_schema_valid"`
	SchemaSupported      bool          `json:"schema_supported"`
	RuntimeProtocolValid bool          `json:"runtime_protocol_valid"`
	Errors               []string      `json:"errors,omitempty"`
	RuntimeErrors        []string      `json:"runtime_errors,omitempty"`
}

// blindToolCall deliberately omits the provider-generated call ID and raw
// envelope type. Call ID formats are provider fingerprints and can silently
// unblind a model comparison. Valid arguments are canonicalized before they
// are written so whitespace and object-key ordering cannot identify a route.
type blindToolCall struct {
	Name          string `json:"name,omitempty"`
	Arguments     string `json:"arguments,omitempty"`
	EnvelopeValid bool   `json:"envelope_valid"`
	ParseError    string `json:"parse_error,omitempty"`
}

type blindEvalCandidate struct {
	Label       string `json:"label"`
	ContentFile string `json:"content_file"`
}

type blindEvalKey struct {
	SchemaVersion int                `json:"schema_version"`
	Entries       []blindEvalKeyItem `json:"entries"`
}

type blindEvalKeyItem struct {
	CaseID  string `json:"case_id"`
	Lane    string `json:"lane"`
	Label   string `json:"label"`
	Variant string `json:"variant"`
}

func runEvalLaplace(cmd *cobra.Command, _ []string) error {
	manifestPath, _ := cmd.Flags().GetString("manifest")
	variantsPath, _ := cmd.Flags().GetString("variants-file")
	variants, _ := cmd.Flags().GetStringSlice("variant")
	outDir, _ := cmd.Flags().GetString("out")
	blindOut, _ := cmd.Flags().GetString("blind-out")
	requestTimeout, _ := cmd.Flags().GetDuration("request-timeout")
	if manifestPath == "" || variantsPath == "" || outDir == "" || blindOut == "" {
		return fmt.Errorf("--manifest, --variants-file, --out and --blind-out are required")
	}
	if len(variants) < 2 || len(variants) > 26 {
		return fmt.Errorf("--variant requires between 2 and 26 unique names")
	}
	if hasDuplicates(variants) {
		return fmt.Errorf("--variant names must be unique")
	}
	manifest, manifestDir, err := loadLaplaceEvalManifest(manifestPath)
	if err != nil {
		return err
	}
	if err := validateEvalManifest(manifest); err != nil {
		return err
	}
	specs, err := loadVariantSpecs(variantsPath)
	if err != nil {
		return err
	}
	if err := validateEvalVariants(variants, specs); err != nil {
		return err
	}
	endpoint, apiKey, err := llmEndpoint()
	if err != nil {
		return err
	}
	if err := validateOpenRouterEvalEndpoint(endpoint); err != nil {
		return err
	}
	if err := validateDisjointNewEvalDirs(outDir, blindOut); err != nil {
		return err
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return fmt.Errorf("create --out: %w", err)
	}
	if err := os.MkdirAll(blindOut, 0o700); err != nil {
		return fmt.Errorf("create --blind-out: %w", err)
	}
	filesDir := resolveEvalPath(manifestDir, manifest.FilesDir)
	sharedSpec := specs[variants[0]]
	var results []laplaceEvalResult
	blindIndex := blindEvalIndex{SchemaVersion: 1}
	key := blindEvalKey{SchemaVersion: 1}
	failed := 0
	generations := 0
	for _, evalCase := range manifest.Cases {
		tracePath := resolveEvalPath(manifestDir, evalCase.TraceFile)
		traceBytes, err := os.ReadFile(tracePath) // #nosec G304 -- private operator manifest
		if err != nil {
			return fmt.Errorf("case %s: read trace: %w", evalCase.ID, err)
		}
		for _, lane := range evalCase.Lanes {
			if lane.Kind == "captured_tool_loop" {
				loopResults, bundleDirs, keyEntries, loopFailed, loopGenerations, loopErr := runCapturedToolLoopLane(
					cmd.Context(), evalCase, lane, traceBytes, variants, specs, endpoint, apiKey,
					filesDir, outDir, blindOut, sharedSpec, requestTimeout,
				)
				if loopErr != nil {
					return loopErr
				}
				results = append(results, loopResults...)
				blindIndex.Bundles = append(blindIndex.Bundles, bundleDirs...)
				key.Entries = append(key.Entries, keyEntries...)
				failed += loopFailed
				generations += loopGenerations
				continue
			}
			bodyStr, pickedIdx, nTurns, err := selectGenRequestBody(traceBytes, evalCase.Agent, lane.Gen)
			if err != nil {
				return fmt.Errorf("case %s lane %s: %w", evalCase.ID, lane.Name, err)
			}
			caseDir := filepath.Join(outDir, "runs", evalCase.ID, lane.Name)
			blindDir := filepath.Join(blindOut, "cases", evalCase.ID, lane.Name)
			if err := os.MkdirAll(caseDir, 0o700); err != nil {
				return fmt.Errorf("case %s: create run directory: %w", evalCase.ID, err)
			}
			if err := os.MkdirAll(blindDir, 0o700); err != nil {
				return fmt.Errorf("case %s: create blind directory: %w", evalCase.ID, err)
			}
			judgeRequest, err := prepareJudgeRequest(bodyStr, sharedSpec, lane.ForceToolChoiceNone)
			if err != nil {
				return fmt.Errorf("case %s lane %s: prepare judge request: %w", evalCase.ID, lane.Name, err)
			}
			if err := os.MkdirAll(filepath.Join(blindDir, "media"), 0o700); err != nil {
				return fmt.Errorf("case %s: create blind media directory: %w", evalCase.ID, err)
			}
			if err := stageBlindJudgeMedia(judgeRequest, filesDir, filepath.Join(blindDir, "media")); err != nil {
				return fmt.Errorf("case %s lane %s: stage judge media: %w", evalCase.ID, lane.Name, err)
			}
			if err := writePrivateRootFile(blindDir, "request.json", judgeRequest); err != nil {
				return fmt.Errorf("case %s: write judge request: %w", evalCase.ID, err)
			}
			requestPath := filepath.Join(blindDir, "request.json")

			order, err := shuffledStrings(variants)
			if err != nil {
				return fmt.Errorf("randomize variants: %w", err)
			}
			blindCase := blindEvalCase{
				CaseID: evalCase.ID, Category: evalCase.Category, Subtype: evalCase.Subtype,
				Lane: lane.Name, RequestFile: relativeEvalPath(blindDir, requestPath),
			}
			for labelIndex, name := range order {
				spec := specs[name]
				if lane.ForceToolChoiceNone {
					spec.ForceToolChoiceNone = true
				}
				body, prepErr := prepareBody(bodyStr, spec, filesDir, "", false)
				var replay replayRunResult
				if prepErr != nil {
					replay = replayRunResult{Variant: name, Run: 1, Err: prepErr.Error()}
				} else {
					variantDir := filepath.Join(caseDir, safeFileComponent(name))
					if err := os.MkdirAll(variantDir, 0o700); err != nil {
						return fmt.Errorf("case %s: create variant directory: %w", evalCase.ID, err)
					}
					replay = postOnce(cmd.Context(), endpoint, apiKey, body, name, 1, variantDir, requestTimeout)
				}
				if replay.RouteMismatch {
					return fmt.Errorf("case %s lane %s: provider returned a different model or route", evalCase.ID, lane.Name)
				}
				generations++
				replay.Agent = evalCase.Agent
				replay.GenSelector = lane.Gen
				replay.GenIndex = pickedIdx
				replay.GenTurns = nTurns
				if replay.Err != "" || replay.ProtocolFailure || replay.RouteMismatch {
					failed++
				}
				results = append(results, laplaceEvalResult{
					CaseID: evalCase.ID, Category: evalCase.Category, Subtype: evalCase.Subtype,
					UserSlot: evalCase.UserSlot, Lane: lane.Name, Replay: &replay,
				})

				label := string(rune('A' + labelIndex))
				blindPath := filepath.Join(blindDir, label+".md")
				if err := writeBlindCandidate(blindPath, replay); err != nil {
					return fmt.Errorf("case %s: write blind candidate: %w", evalCase.ID, err)
				}
				blindCase.Candidates = append(blindCase.Candidates, blindEvalCandidate{
					Label: label, ContentFile: relativeEvalPath(blindDir, blindPath),
				})
				key.Entries = append(key.Entries, blindEvalKeyItem{
					CaseID: evalCase.ID, Lane: lane.Name, Label: label, Variant: name,
				})
				fmt.Printf("[%s/%s/%s] status=%s tokens=%d+%d reasoning=%d cost=%s duration=%s\n",
					evalCase.ID, lane.Name, label, replayStatus(replay), replay.PromptTokens,
					replay.CompletionTokens, replay.ReasoningTokens, formatOptionalCost(replay.CostUSD),
					time.Duration(replay.DurationMS)*time.Millisecond)
			}
			bundleManifest := blindEvalManifest{SchemaVersion: 1, MediaDir: "media", Cases: []blindEvalCase{blindCase}}
			if err := writePrivateJSON(filepath.Join(blindDir, "manifest.json"), bundleManifest); err != nil {
				return err
			}
			blindIndex.Bundles = append(blindIndex.Bundles, relativeEvalPath(blindOut, blindDir))
		}
	}

	if err := writePrivateJSON(filepath.Join(outDir, "summary.json"), results); err != nil {
		return err
	}
	if err := writePrivateJSON(filepath.Join(blindOut, "index.json"), blindIndex); err != nil {
		return err
	}
	protocol := "JUDGE ACCESS PROTOCOL\n\n" +
		"This root and index.json are operator-only. Never give a judge this directory or index.\n" +
		"Give a judge exactly one bundle directory listed in index.json. Each bundle is self-contained.\n" +
		"Do not let a judge inspect sibling bundles: later teacher-forced requests can reveal earlier reference decisions.\n"
	if err := os.WriteFile(filepath.Join(blindOut, "JUDGE_PROTOCOL.txt"), []byte(protocol), 0o600); err != nil {
		return fmt.Errorf("write judge access protocol: %w", err)
	}
	if err := writePrivateJSON(filepath.Join(outDir, "blind_key.json"), key); err != nil {
		return err
	}
	fmt.Printf("Eval complete: cases=%d judge_units=%d generations=%d failures=%d out=%s operator_bundle_root=%s\n",
		len(manifest.Cases), len(blindIndex.Bundles), generations, failed, outDir, blindOut)
	fmt.Println("Judge access: give exactly one bundle directory from index.json; never share the root/index or sibling bundles.")
	return nil
}

func validateOpenRouterEvalEndpoint(endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("parse eval endpoint: %w", err)
	}
	if parsed.Scheme != "https" || parsed.User != nil || !strings.EqualFold(parsed.Hostname(), "openrouter.ai") ||
		(parsed.Port() != "" && parsed.Port() != "443") ||
		parsed.Path != "/api/v1/chat/completions" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("eval-laplace requires the official HTTPS OpenRouter chat-completions endpoint")
	}
	return nil
}

func runCapturedToolLoopLane(
	ctx context.Context,
	evalCase laplaceEvalCase,
	lane laplaceEvalLane,
	traceBytes []byte,
	variants []string,
	specs map[string]variantSpec,
	endpoint, apiKey, filesDir, outDir, blindOut string,
	sharedSpec variantSpec,
	requestTimeout time.Duration,
) ([]laplaceEvalResult, []string, []blindEvalKeyItem, int, int, error) {
	turns, err := collectGenTurns(traceBytes, evalCase.Agent)
	if err != nil {
		return nil, nil, nil, 0, 0, fmt.Errorf("case %s lane %s: collect turns: %w", evalCase.ID, lane.Name, err)
	}
	if len(turns) == 0 {
		return nil, nil, nil, 0, 0, fmt.Errorf("case %s lane %s: no captured generation turns", evalCase.ID, lane.Name)
	}
	expected := make([]replayAssistantOutput, len(turns))
	judgeRequests := make([][]byte, len(turns))
	for i, turn := range turns {
		if turn.ResponseBody == "" {
			return nil, nil, nil, 0, 0, fmt.Errorf("case %s lane %s turn %d: missing llm.response", evalCase.ID, lane.Name, i)
		}
		expected[i], err = firstReplayAssistantOutput([]byte(turn.ResponseBody))
		if err != nil {
			return nil, nil, nil, 0, 0, fmt.Errorf("case %s lane %s turn %d: parse llm.response: %w", evalCase.ID, lane.Name, i, err)
		}
		judgeRequest, judgeErr := prepareJudgeRequest(turn.RequestBody, sharedSpec, false)
		if judgeErr != nil {
			return nil, nil, nil, 0, 0, fmt.Errorf("case %s lane %s turn %d: prepare judge request: %w", evalCase.ID, lane.Name, i, judgeErr)
		}
		judgeRequests[i] = judgeRequest
	}

	caseDir := filepath.Join(outDir, "runs", evalCase.ID, lane.Name)
	blindDir := filepath.Join(blindOut, "cases", evalCase.ID, lane.Name)
	if err := os.MkdirAll(caseDir, 0o700); err != nil {
		return nil, nil, nil, 0, 0, fmt.Errorf("case %s: create run directory: %w", evalCase.ID, err)
	}
	if err := os.MkdirAll(blindDir, 0o700); err != nil {
		return nil, nil, nil, 0, 0, fmt.Errorf("case %s: create blind directory: %w", evalCase.ID, err)
	}

	var results []laplaceEvalResult
	loops := make(map[string]toolLoopRunResult, len(variants))
	failed := 0
	generations := 0
	generationOrder, err := shuffledStrings(variants)
	if err != nil {
		return nil, nil, nil, 0, 0, fmt.Errorf("randomize generation order: %w", err)
	}
	for _, name := range generationOrder {
		variantDir := filepath.Join(caseDir, safeFileComponent(name))
		if err := os.MkdirAll(variantDir, 0o700); err != nil {
			return nil, nil, nil, 0, 0, fmt.Errorf("case %s: create variant directory: %w", evalCase.ID, err)
		}
		loop := toolLoopRunResult{}
		for i, turn := range turns {
			body, prepErr := prepareBody(turn.RequestBody, specs[name], filesDir, "", false)
			assessmentBody := body
			var replay replayRunResult
			if prepErr != nil {
				replay = replayRunResult{Variant: name, Run: 1, Err: prepErr.Error()}
				assessmentBody = []byte(turn.RequestBody)
			} else {
				replay = postOnceAtTurn(ctx, endpoint, apiKey, body, name, 1, i, variantDir, requestTimeout)
			}
			if replay.RouteMismatch {
				return nil, nil, nil, 0, 0, fmt.Errorf("case %s lane %s turn %d: provider returned a different model or route", evalCase.ID, lane.Name, i)
			}
			generations++
			replay.Agent = evalCase.Agent
			replay.GenSelector = "all"
			replay.GenIndex = i
			replay.GenTurns = len(turns)
			assessment, assessErr := assessToolTurn(assessmentBody, expected[i], replay)
			if assessErr != nil {
				return nil, nil, nil, 0, 0, fmt.Errorf("case %s lane %s turn %d: assess calls: %w", evalCase.ID, lane.Name, i, assessErr)
			}
			if assessment.DecisionMatch {
				loop.DecisionMatches++
			}
			if assessment.ExactCallMatch {
				loop.ExactMatches++
			}
			if assessment.ActualDecision == toolDecisionMalformed {
				loop.MalformedTurns++
			}
			if replay.Err != "" || replay.ProtocolFailure || replay.RouteMismatch ||
				(assessment.ExpectedDecisionAssessable && !assessment.DecisionMatch) ||
				assessment.ActualDecision == toolDecisionMalformed {
				failed++
			}
			loop.Turns = append(loop.Turns, toolLoopTurnResult{Turn: i, Replay: replay, Assessment: assessment})
		}
		results = append(results, laplaceEvalResult{
			CaseID: evalCase.ID, Category: evalCase.Category, Subtype: evalCase.Subtype,
			UserSlot: evalCase.UserSlot, Lane: lane.Name, ToolLoop: &loop,
		})
		loops[name] = loop
		fmt.Printf("[%s/%s/%s] turns=%d decision_matches=%d exact_matches=%d malformed=%d\n",
			evalCase.ID, lane.Name, name, len(loop.Turns), loop.DecisionMatches, loop.ExactMatches, loop.MalformedTurns)
	}

	// Each tool decision is a separate blind unit. A later teacher-forced
	// request necessarily contains the captured call/result from earlier turns;
	// bundling all requests would therefore leak the reference answer for those
	// earlier turns to the judge.
	var bundleDirs []string
	var keyEntries []blindEvalKeyItem
	for i, judgeRequest := range judgeRequests {
		turnLane := fmt.Sprintf("%s-turn-%03d", lane.Name, i)
		turnDir := filepath.Join(blindDir, fmt.Sprintf("turn-%03d", i))
		if err := os.MkdirAll(turnDir, 0o700); err != nil {
			return nil, nil, nil, 0, 0, fmt.Errorf("case %s turn %d: create blind directory: %w", evalCase.ID, i, err)
		}
		if err := os.MkdirAll(filepath.Join(turnDir, "media"), 0o700); err != nil {
			return nil, nil, nil, 0, 0, fmt.Errorf("case %s turn %d: create blind media directory: %w", evalCase.ID, i, err)
		}
		if err := stageBlindJudgeMedia(judgeRequest, filesDir, filepath.Join(turnDir, "media")); err != nil {
			return nil, nil, nil, 0, 0, fmt.Errorf("case %s lane %s turn %d: stage judge media: %w", evalCase.ID, lane.Name, i, err)
		}
		requestPath := filepath.Join(turnDir, "request.json")
		if err := os.WriteFile(requestPath, judgeRequest, 0o600); err != nil {
			return nil, nil, nil, 0, 0, fmt.Errorf("case %s turn %d: write blind request: %w", evalCase.ID, i, err)
		}
		order, shuffleErr := shuffledStrings(variants)
		if shuffleErr != nil {
			return nil, nil, nil, 0, 0, fmt.Errorf("randomize variants: %w", shuffleErr)
		}
		blindCase := blindEvalCase{
			CaseID: evalCase.ID, Category: evalCase.Category, Subtype: evalCase.Subtype,
			Lane: turnLane, RequestFile: relativeEvalPath(turnDir, requestPath),
		}
		for labelIndex, name := range order {
			loop := loops[name]
			if i >= len(loop.Turns) {
				return nil, nil, nil, 0, 0, fmt.Errorf("case %s turn %d: missing variant result", evalCase.ID, i)
			}
			label := string(rune('A' + labelIndex))
			blindPath := filepath.Join(turnDir, label+".json")
			turnOnly := toolLoopRunResult{Turns: []toolLoopTurnResult{loop.Turns[i]}}
			if loop.Turns[i].Assessment.ActualDecision == toolDecisionMalformed {
				turnOnly.MalformedTurns = 1
			}
			if err := writeBlindToolCandidate(blindPath, turnOnly); err != nil {
				return nil, nil, nil, 0, 0, fmt.Errorf("case %s turn %d: write blind tool candidate: %w", evalCase.ID, i, err)
			}
			blindCase.Candidates = append(blindCase.Candidates, blindEvalCandidate{
				Label: label, ContentFile: relativeEvalPath(turnDir, blindPath),
			})
			keyEntries = append(keyEntries, blindEvalKeyItem{
				CaseID: evalCase.ID, Lane: turnLane, Label: label, Variant: name,
			})
		}
		bundleManifest := blindEvalManifest{SchemaVersion: 1, MediaDir: "media", Cases: []blindEvalCase{blindCase}}
		if err := writePrivateJSON(filepath.Join(turnDir, "manifest.json"), bundleManifest); err != nil {
			return nil, nil, nil, 0, 0, err
		}
		bundleDirs = append(bundleDirs, relativeEvalPath(blindOut, turnDir))
	}
	return results, bundleDirs, keyEntries, failed, generations, nil
}

func writeBlindToolCandidate(path string, loop toolLoopRunResult) error {
	packet := blindToolCandidatePacket{
		Kind:           "captured_tool_loop",
		MalformedTurns: loop.MalformedTurns,
	}
	for _, turn := range loop.Turns {
		var content string
		if turn.Replay.Err == "" && !turn.Replay.RouteMismatch && !turn.Replay.ProtocolFailure {
			var err error
			content, err = readBlindReplayContent(turn.Replay)
			if err != nil {
				return err
			}
		}
		packet.Turns = append(packet.Turns, blindToolCandidateTurn{
			Turn:           turn.Turn,
			Status:         replayStatus(turn.Replay),
			Content:        content,
			ActualDecision: turn.Assessment.ActualDecision,
			ActualCalls:    blindToolCallAssessments(turn.Assessment.ActualCalls),
		})
	}
	return writePrivateJSON(path, packet)
}

func blindToolCallAssessments(calls []toolCallAssessment) []blindToolCallAssessment {
	result := make([]blindToolCallAssessment, 0, len(calls))
	for _, call := range calls {
		arguments := call.Call.Arguments
		if canonical, err := canonicalToolArguments(arguments); err == nil {
			arguments = string(canonical)
		}
		result = append(result, blindToolCallAssessment{
			Call: blindToolCall{
				Name:          call.Call.Name,
				Arguments:     arguments,
				EnvelopeValid: call.Call.EnvelopeValid,
				ParseError:    call.Call.ParseError,
			},
			KnownTool:            call.KnownTool,
			ArgumentsJSONValid:   call.ArgumentsJSONValid,
			ArgumentsSchemaValid: call.ArgumentsSchemaValid,
			SchemaSupported:      call.SchemaSupported,
			RuntimeProtocolValid: call.RuntimeProtocolValid,
			Errors:               call.Errors,
			RuntimeErrors:        call.RuntimeErrors,
		})
	}
	return result
}

func loadLaplaceEvalManifest(path string) (laplaceEvalManifest, string, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- operator-supplied private manifest
	if err != nil {
		return laplaceEvalManifest{}, "", fmt.Errorf("read eval manifest: %w", err)
	}
	var manifest laplaceEvalManifest
	if err := decodeStrictJSONDocument(data, &manifest); err != nil {
		return laplaceEvalManifest{}, "", fmt.Errorf("parse eval manifest: %w", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return laplaceEvalManifest{}, "", fmt.Errorf("resolve eval manifest: %w", err)
	}
	if len(manifest.Cases) == 0 {
		return laplaceEvalManifest{}, "", fmt.Errorf("eval manifest has no cases")
	}
	return manifest, filepath.Dir(abs), nil
}

func validateEvalVariant(name string, spec variantSpec) error {
	if spec.Model == "" {
		return fmt.Errorf("variant %q must set model", name)
	}
	if len(spec.ProviderOnly) != 1 || spec.AllowFallbacks == nil || *spec.AllowFallbacks ||
		spec.DataCollection != "deny" || spec.ZeroDataRetention == nil || !*spec.ZeroDataRetention ||
		spec.RequireParameters == nil || !*spec.RequireParameters {
		return fmt.Errorf("variant %q must pin one provider with allow_fallbacks=false, data_collection=deny, zdr=true and require_parameters=true", name)
	}
	if spec.DisableTools || spec.ForceToolChoiceNone {
		return fmt.Errorf("variant %q must not set tool-mode fields; lanes control tool policy", name)
	}
	return nil
}

func validateEvalVariants(names []string, specs map[string]variantSpec) error {
	components := make(map[string]string, len(names))
	var sharedFingerprint string
	for i, name := range names {
		spec, ok := specs[name]
		if !ok {
			return fmt.Errorf("variant %q is missing from --variants-file", name)
		}
		if err := validateEvalVariant(name, spec); err != nil {
			return err
		}
		component := safeFileComponent(name)
		if previous, exists := components[component]; exists {
			return fmt.Errorf("variant names %q and %q collide as filesystem component %q", previous, name, component)
		}
		components[component] = name
		fingerprint, err := evalSemanticTransformFingerprint(spec)
		if err != nil {
			return fmt.Errorf("variant %q: compare semantic transforms: %w", name, err)
		}
		if i == 0 {
			sharedFingerprint = fingerprint
		} else if fingerprint != sharedFingerprint {
			return fmt.Errorf("variant %q changes task semantics; eval variants must share media/prompt/fact transforms and max_tokens", name)
		}
	}
	return nil
}

func evalSemanticTransformFingerprint(spec variantSpec) (string, error) {
	// Zero only provider/model transport knobs. The remaining fields alter the
	// task or its available evidence and therefore must match across variants.
	spec.Model = ""
	spec.ReasoningEffort = ""
	spec.ClearReasoning = false
	spec.StripMessageReasoning = false
	spec.ProviderOrder = nil
	spec.ProviderOnly = nil
	spec.AllowFallbacks = nil
	spec.DataCollection = ""
	spec.ZeroDataRetention = nil
	spec.RequireParameters = nil
	spec.ClearProvider = false
	spec.ImageInputFormat = ""
	data, err := json.Marshal(spec)
	return string(data), err
}

func validateEvalManifest(manifest laplaceEvalManifest) error {
	if manifest.SchemaVersion != 1 {
		return fmt.Errorf("unsupported eval manifest schema_version %d", manifest.SchemaVersion)
	}
	seen := make(map[string]struct{}, len(manifest.Cases))
	for _, evalCase := range manifest.Cases {
		if err := validateEvalCase(evalCase); err != nil {
			return err
		}
		if _, exists := seen[evalCase.ID]; exists {
			return fmt.Errorf("duplicate eval case id %q", evalCase.ID)
		}
		seen[evalCase.ID] = struct{}{}
	}
	return nil
}

func validateDisjointNewEvalDirs(privateOut, blindOut string) error {
	privateAbs, err := canonicalNewEvalPath(privateOut)
	if err != nil {
		return fmt.Errorf("resolve --out: %w", err)
	}
	blindAbs, err := canonicalNewEvalPath(blindOut)
	if err != nil {
		return fmt.Errorf("resolve --blind-out: %w", err)
	}
	if evalPathContains(privateAbs, blindAbs) || evalPathContains(blindAbs, privateAbs) {
		return fmt.Errorf("--out and --blind-out must be disjoint directory trees")
	}
	for flag, path := range map[string]string{"--out": privateAbs, "--blind-out": blindAbs} {
		if _, statErr := os.Stat(path); statErr == nil {
			return fmt.Errorf("%s already exists: %s", flag, path)
		} else if !os.IsNotExist(statErr) {
			return fmt.Errorf("inspect %s: %w", flag, statErr)
		}
	}
	return nil
}

// canonicalNewEvalPath resolves symlinks in the closest existing ancestor,
// then appends the not-yet-created suffix. Eval output roots must be new, so a
// direct EvalSymlinks call on the full path would otherwise always fail.
func canonicalNewEvalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	probe := filepath.Clean(abs)
	var missing []string
	for {
		if _, statErr := os.Lstat(probe); statErr == nil {
			break
		} else if !os.IsNotExist(statErr) {
			return "", statErr
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return "", fmt.Errorf("no existing ancestor")
		}
		missing = append(missing, filepath.Base(probe))
		probe = parent
	}
	resolved, err := filepath.EvalSymlinks(probe)
	if err != nil {
		return "", err
	}
	for i := len(missing) - 1; i >= 0; i-- {
		resolved = filepath.Join(resolved, missing[i])
	}
	return filepath.Clean(resolved), nil
}

func evalPathContains(parent, child string) bool {
	rel, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func validateEvalCase(evalCase laplaceEvalCase) error {
	if evalCase.ID == "" || safeFileComponent(evalCase.ID) != evalCase.ID {
		return fmt.Errorf("invalid eval case id %q", evalCase.ID)
	}
	if evalCase.TraceFile == "" || evalCase.Agent == "" || len(evalCase.Lanes) == 0 {
		return fmt.Errorf("case %s must set trace_file, agent and lanes", evalCase.ID)
	}
	seen := map[string]bool{}
	for _, lane := range evalCase.Lanes {
		if lane.Name == "" || safeFileComponent(lane.Name) != lane.Name || seen[lane.Name] {
			return fmt.Errorf("case %s has invalid or duplicate lane %q", evalCase.ID, lane.Name)
		}
		if lane.Kind != "" && lane.Kind != "single" && lane.Kind != "captured_tool_loop" {
			return fmt.Errorf("case %s lane %s has unsupported kind %q", evalCase.ID, lane.Name, lane.Kind)
		}
		if lane.Kind == "captured_tool_loop" {
			if lane.ForceToolChoiceNone {
				return fmt.Errorf("case %s lane %s: captured_tool_loop cannot force tool_choice none", evalCase.ID, lane.Name)
			}
			if lane.Gen != "" && lane.Gen != "all" {
				return fmt.Errorf("case %s lane %s: captured_tool_loop gen must be empty or all", evalCase.ID, lane.Name)
			}
		}
		seen[lane.Name] = true
	}
	return nil
}

func prepareJudgeRequest(bodyStr string, sharedSpec variantSpec, forceToolChoiceNone bool) ([]byte, error) {
	var body map[string]any
	if err := json.Unmarshal([]byte(bodyStr), &body); err != nil {
		return nil, err
	}
	if sharedSpec.DropMedia {
		dropMediaParts(body)
	}
	if len(sharedSpec.DropMediaMIMETypes) > 0 {
		dropMediaPartsByMIME(body, sharedSpec.DropMediaMIMETypes)
	}
	for _, tag := range sharedSpec.StripSystemTags {
		stripSystemTag(body, tag)
	}
	if sharedSpec.KeepFactIDs != nil {
		keepFactIDs(body, sharedSpec.KeepFactIDs)
	}
	if sharedSpec.SetUserText != nil {
		setUserText(body, *sharedSpec.SetUserText)
	}
	if sharedSpec.InsertBeforeCurrentMedia != "" {
		insertCurrentMediaMarker(body, sharedSpec.InsertBeforeCurrentMedia)
	}
	for _, replacement := range sharedSpec.SystemReplace {
		replaceInSystem(body, replacement.Find, replacement.With)
	}
	if sharedSpec.MaxTokens > 0 {
		body["max_tokens"] = sharedSpec.MaxTokens
	}
	for _, key := range []string{
		"model", "models", "route", "provider", "reasoning", "trace", "user",
		"session_id", "safety_identifier", "metadata", "stream",
	} {
		delete(body, key)
	}
	if forceToolChoiceNone {
		if !hasRole(body, "tool") {
			return nil, fmt.Errorf("frozen synthesis lane has no tool result")
		}
		body["tool_choice"] = "none"
	}
	msgs, _ := body["messages"].([]any)
	for _, msg := range msgs {
		if m, ok := msg.(map[string]any); ok {
			delete(m, "reasoning_details")
		}
	}
	neutralizeJudgeToolCallIDs(body)
	return json.MarshalIndent(body, "", "  ")
}

// stageBlindJudgeMedia copies only hash- and size-verified blobs referenced by
// a sanitized judge request into the isolated blind bundle. Requests retain
// redacted placeholders so the bundle never embeds large base64 payloads or a
// path back into the private replay directory.
func stageBlindJudgeMedia(judgeRequest []byte, filesDir, mediaOut string) error {
	var body any
	if err := json.Unmarshal(judgeRequest, &body); err != nil {
		return fmt.Errorf("parse sanitized judge request: %w", err)
	}
	seen := map[string]int64{}
	var walk func(any) error
	walk = func(value any) error {
		switch typed := value.(type) {
		case map[string]any:
			for _, child := range typed {
				if err := walk(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range typed {
				if err := walk(child); err != nil {
					return err
				}
			}
		case string:
			match := redactedMediaRe.FindStringSubmatch(typed)
			if match == nil {
				return nil
			}
			hash, mime := match[1], match[2]
			expectedSize, err := strconv.ParseInt(match[3], 10, 64)
			if err != nil || expectedSize < 0 {
				return fmt.Errorf("invalid media size for sha256 %s", hash)
			}
			target := filepath.Join(mediaOut, hash+blindMediaExtension(mime))
			if previousSize, ok := seen[target]; ok {
				if previousSize != expectedSize {
					return fmt.Errorf("conflicting sizes for blind media sha256 %s", hash)
				}
				return nil
			}
			raw, err := readVerifiedMedia(filesDir, hash, expectedSize)
			if err != nil {
				return err
			}
			if err := writeVerifiedBlindMedia(target, raw); err != nil {
				return err
			}
			seen[target] = expectedSize
		}
		return nil
	}
	return walk(body)
}

func blindMediaExtension(mime string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "application/pdf":
		return ".pdf"
	case "video/mp4":
		return ".mp4"
	case "audio/ogg":
		return ".ogg"
	case "audio/wav", "audio/x-wav":
		return ".wav"
	default:
		return ".bin"
	}
}

func writeVerifiedBlindMedia(path string, raw []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- hash-derived path in new blind output
	if err == nil {
		if _, writeErr := file.Write(raw); writeErr != nil {
			_ = file.Close()
			return fmt.Errorf("stage blind media: %w", writeErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			return fmt.Errorf("stage blind media: %w", closeErr)
		}
		return nil
	}
	if !os.IsExist(err) {
		return fmt.Errorf("stage blind media: %w", err)
	}
	info, statErr := os.Lstat(path)
	if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("existing blind media target is not a regular file")
	}
	existing, readErr := os.ReadFile(path) // #nosec G304 -- hash-derived path in new blind output
	if readErr != nil {
		return fmt.Errorf("verify existing blind media: %w", readErr)
	}
	if !bytes.Equal(existing, raw) {
		return fmt.Errorf("existing blind media target has different content")
	}
	if chmodErr := os.Chmod(path, 0o600); chmodErr != nil {
		return fmt.Errorf("restrict existing blind media: %w", chmodErr)
	}
	return nil
}

// neutralizeJudgeToolCallIDs preserves the linkage between an assistant tool
// call and its frozen tool result while removing the provider-specific ID
// format. These IDs are transport metadata, not task evidence, and can reveal
// which provider produced the captured conversation even when both candidates
// share the same request packet.
func neutralizeJudgeToolCallIDs(body map[string]any) {
	messages, _ := body["messages"].([]any)
	mapping := map[string]string{}
	next := 0
	neutral := func(original string) string {
		if replacement := mapping[original]; replacement != "" {
			return replacement
		}
		next++
		replacement := fmt.Sprintf("blind_tool_call_%03d", next)
		mapping[original] = replacement
		return replacement
	}

	// Build the mapping from assistant envelopes first so tool-result messages
	// receive the same neutral ID even if their wire order is unusual.
	for _, rawMessage := range messages {
		message, _ := rawMessage.(map[string]any)
		calls, _ := message["tool_calls"].([]any)
		for _, rawCall := range calls {
			call, _ := rawCall.(map[string]any)
			original, _ := call["id"].(string)
			if original == "" {
				continue
			}
			call["id"] = neutral(original)
		}
	}
	for _, rawMessage := range messages {
		message, _ := rawMessage.(map[string]any)
		original, _ := message["tool_call_id"].(string)
		if original == "" {
			continue
		}
		message["tool_call_id"] = neutral(original)
	}
}

func writeBlindCandidate(path string, replay replayRunResult) error {
	if replay.Err != "" || replay.RouteMismatch || replay.ProtocolFailure {
		return os.WriteFile(path, []byte("[generation invalidated]\n"), 0o600)
	}
	content, err := readBlindReplayContent(replay)
	if err != nil {
		return err
	}
	output := []byte(content)
	if len(replay.ToolCallDetails) > 0 {
		calls := make([]blindToolCall, 0, len(replay.ToolCallDetails))
		for _, call := range replay.ToolCallDetails {
			arguments := call.Arguments
			if canonical, canonicalErr := canonicalToolArguments(arguments); canonicalErr == nil {
				arguments = string(canonical)
			}
			calls = append(calls, blindToolCall{
				Name: call.Name, Arguments: arguments,
				EnvelopeValid: call.EnvelopeValid, ParseError: call.ParseError,
			})
		}
		callJSON, marshalErr := json.MarshalIndent(calls, "", "  ")
		if marshalErr != nil {
			return marshalErr
		}
		if len(output) > 0 {
			output = append(output, []byte("\n\n")...)
		}
		output = append(output, []byte("[tool calls]\n")...)
		output = append(output, callJSON...)
	}
	if len(output) == 0 {
		output = []byte("[empty generation]\n")
	}
	return os.WriteFile(path, output, 0o600)
}

func readBlindReplayContent(replay replayRunResult) (string, error) {
	if replay.RawFile != "" {
		raw, err := os.ReadFile(replay.RawFile) // #nosec G304 -- path produced by this eval run
		if err != nil {
			return "", err
		}
		parsed, err := parseReplayAPIResponse(raw)
		if err != nil {
			return "", err
		}
		if len(parsed.Choices) > 0 {
			return string(replayContentBytes(parsed.Choices[0].Message.Content, nil)), nil
		}
	}
	if replay.ToolCalls > 0 {
		// ContentFile also contains a diagnostic dump of raw tool envelopes.
		// Never copy that provider fingerprint into the judge bundle.
		return "", nil
	}
	if replay.ContentFile == "" {
		return "", nil
	}
	content, err := os.ReadFile(replay.ContentFile) // #nosec G304 -- path produced by this eval run
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func writePrivateJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// writePrivateRootFile keeps judge-bundle writes beneath the already-created
// 0700 bundle directory. The filename is interpreted relative to an os.Root,
// so even a future caller cannot escape through ".." or an outward symlink.
func writePrivateRootFile(rootDir, name string, data []byte) error {
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return fmt.Errorf("open private output root: %w", err)
	}
	writeErr := root.WriteFile(name, data, 0o600)
	closeErr := root.Close()
	if writeErr != nil {
		return fmt.Errorf("write private output: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close private output root: %w", closeErr)
	}
	return nil
}

func shuffledStrings(values []string) ([]string, error) {
	out := append([]string(nil), values...)
	for i := len(out) - 1; i > 0; i-- {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return nil, err
		}
		j := int(n.Int64())
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func resolveEvalPath(base, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(base, path)
}

func relativeEvalPath(base, path string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return path
	}
	return rel
}

func hasDuplicates(values []string) bool {
	seen := map[string]bool{}
	for _, value := range values {
		if seen[value] {
			return true
		}
		seen[value] = true
	}
	return false
}

func replayStatus(replay replayRunResult) string {
	switch {
	case replay.Err != "":
		return "error"
	case replay.RouteMismatch:
		return "route_mismatch"
	case replay.ProtocolFailure:
		return "protocol_failure"
	default:
		return "ok"
	}
}
