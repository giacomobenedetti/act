package runner

import (
	"context"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/nektos/act/pkg/common"
	"github.com/nektos/act/pkg/container"
	"github.com/nektos/act/pkg/exprparser"
	"github.com/nektos/act/pkg/model"
)

type step interface {
	pre() common.Executor
	main() common.Executor
	post() common.Executor

	getRunContext() *RunContext
	getGithubContext(ctx context.Context) *model.GithubContext
	getStepModel() *model.Step
	getEnv() *map[string]string
	getIfExpression(context context.Context, stage stepStage) string
}

type stepStage int

const (
	stepStagePre stepStage = iota
	stepStageMain
	stepStagePost
)

func (s stepStage) String() string {
	switch s {
	case stepStagePre:
		return "Pre"
	case stepStageMain:
		return "Main"
	case stepStagePost:
		return "Post"
	}
	return "Unknown"
}

func processRunnerEnvFileCommand(ctx context.Context, fileName string, rc *RunContext, setter func(context.Context, map[string]string, string)) error {
	env := map[string]string{}
	err := rc.JobContainer.UpdateFromEnv(path.Join(rc.JobContainer.GetActPath(), fileName), &env)(ctx)
	if err != nil {
		return err
	}
	for k, v := range env {
		setter(ctx, map[string]string{"name": k}, v)
	}
	return nil
}

func runStepExecutor(step step, stage stepStage, executor common.Executor) common.Executor {
	return func(ctx context.Context) error {
		f, errFile := os.OpenFile("syncfile", os.O_APPEND|os.O_CREATE|os.O_RDWR, 0755)
		defer f.Close()
		logger := common.Logger(ctx)
		rc := step.getRunContext()
		stepModel := step.getStepModel()
		previousStep := func(step *RunContext) *model.Step {
			var prevStep *model.Step
			for _, ps := range step.steps() {
				if ps.ID == rc.CurrentStep {
					prevStep = ps
				}
			}
			return prevStep
		}(rc)

		step.getStepModel().Prev = previousStep

		ifExpression := step.getIfExpression(ctx, stage)
		rc.CurrentStep = stepModel.ID

		stepResult := &model.StepResult{
			Outcome:    model.StepStatusSuccess,
			Conclusion: model.StepStatusSuccess,
			Outputs:    make(map[string]string),
		}
		if stage == stepStageMain {
			rc.StepResults[rc.CurrentStep] = stepResult
		}

		err := setupEnv(ctx, step)
		if err != nil {
			return err
		}

		runStep, err := isStepEnabled(ctx, ifExpression, step, stage)
		if !runStep {
			f.Write([]byte(fmt.Sprintf("\"%s - step %s\" [style=dashed];\n", rc.Run.JobID, stepModel.ID)))
			if previousStep != nil {
				if stepModel.If.Value != "" {
					// if strings.Contains(stepModel.If.Value, "failure") {
					// 	f.Write([]byte(fmt.Sprintf("\"%s - cond %s\" -> \"%s - step %s\";\n", rc.Run.JobID, previousStep.ID, rc.Run.JobID, previousStep.ID)))
					// 	if previousStep.If.Value != "" {
					// 		f.Write([]byte(fmt.Sprintf("\"%s - cond %s\" -> \"%s - cond %s\";\n", rc.Run.JobID, previousStep.Prev.ID, rc.Run.JobID, previousStep.ID)))
					// 		f.Write([]byte(fmt.Sprintf("\"%s - step %s\" -> \"%s - cond %s\";\n", rc.Run.JobID, previousStep.Prev.ID, rc.Run.JobID, previousStep.ID)))
					// 	} else {
					// 		f.Write([]byte(fmt.Sprintf("\"%s - step %s\" -> \"%s - cond %s\";\n", rc.Run.JobID, previousStep.Prev.ID, rc.Run.JobID, previousStep.ID)))
					// 	}
					// }
					f.Write([]byte(fmt.Sprintf("\"%s - cond %s\" -> \"%s - step %s\";\n", rc.Run.JobID, stepModel.ID, rc.Run.JobID, stepModel.ID)))
					if previousStep.If.Value != "" {
						f.Write([]byte(fmt.Sprintf("\"%s - cond %s\" -> \"%s - cond %s\";\n", rc.Run.JobID, previousStep.ID, rc.Run.JobID, stepModel.ID)))
						f.Write([]byte(fmt.Sprintf("\"%s - step %s\" -> \"%s - cond %s\";\n", rc.Run.JobID, previousStep.ID, rc.Run.JobID, stepModel.ID)))
					} else {
						f.Write([]byte(fmt.Sprintf("\"%s - step %s\" -> \"%s - cond %s\";\n", rc.Run.JobID, previousStep.ID, rc.Run.JobID, stepModel.ID)))
					}
				} else {
					// if strings.Contains(stepModel.If.Value, "failure") {
					// 	if previousStep.If.Value != "" {
					// 		f.Write([]byte(fmt.Sprintf("\"%s - cond %s\" -> \"%s - step %s\";\n", rc.Run.JobID, previousStep.Prev.ID, rc.Run.JobID, previousStep.ID)))
					// 	}
					// 	f.Write([]byte(fmt.Sprintf("\"%s - step %s\" -> \"%s - step %s\";\n", rc.Run.JobID, previousStep.Prev.ID, rc.Run.JobID, previousStep.ID)))
					// }
					if previousStep.If.Value != "" {
						f.Write([]byte(fmt.Sprintf("\"%s - cond %s\" -> \"%s - step %s\";\n", rc.Run.JobID, previousStep.ID, rc.Run.JobID, stepModel.ID)))
					}
					f.Write([]byte(fmt.Sprintf("\"%s - step %s\" -> \"%s - step %s\";\n", rc.Run.JobID, previousStep.ID, rc.Run.JobID, stepModel.ID)))
				}
			} else {
				if stepModel.If.Value != "" {
					if rc.Run.Job().If.Value != "" && !rc.Run.Job().CondInserted {
						f.Write([]byte(fmt.Sprintf("\"cond - %s\" -> \"%s - cond %s\";\n", rc.Run.JobID, rc.Run.JobID, stepModel.ID)))
					} else {
						f.Write([]byte(fmt.Sprintf("\"job - %s\" -> \"%s - cond %s\";\n", rc.Run.JobID, rc.Run.JobID, stepModel.ID)))
					}
					f.Write([]byte(fmt.Sprintf("\"%s - cond %s\" -> \"%s - step %s\";\n", rc.Run.JobID, stepModel.ID, rc.Run.JobID, stepModel.ID)))
				} else {
					f.Write([]byte(fmt.Sprintf("\"job - %s\" -> \"%s - step %s\";\n", rc.Run.JobID, rc.Run.JobID, stepModel.ID)))

				}
			}
		}
		// if the step is enabled to run the corresponding CFG node and conditions are displayed
		if runStep {
			log.Infof("\"%s - step %s\"", rc.Run.JobID, stepModel.ID)
			f.Write([]byte(fmt.Sprintf("\"%s - step %s\";\n", rc.Run.JobID, stepModel.ID)))
			if previousStep != nil {
				if stepModel.If.Value != "" {
					// if strings.Contains(stepModel.If.Value, "failure") {
					// 	f.Write([]byte(fmt.Sprintf("\"%s - cond %s\" -> \"%s - step %s\";\n", rc.Run.JobID, previousStep.ID, rc.Run.JobID, previousStep.ID)))
					// 	if previousStep.If.Value != "" {
					// 		f.Write([]byte(fmt.Sprintf("\"%s - cond %s\" -> \"%s - cond %s\";\n", rc.Run.JobID, previousStep.Prev.ID, rc.Run.JobID, previousStep.ID)))
					// 		f.Write([]byte(fmt.Sprintf("\"%s - step %s\" -> \"%s - cond %s\";\n", rc.Run.JobID, previousStep.Prev.ID, rc.Run.JobID, previousStep.ID)))
					// 	} else {
					// 		f.Write([]byte(fmt.Sprintf("\"%s - step %s\" -> \"%s - cond %s\";\n", rc.Run.JobID, previousStep.Prev.ID, rc.Run.JobID, previousStep.ID)))
					// 	}
					// }
					f.Write([]byte(fmt.Sprintf("\"%s - cond %s\" -> \"%s - step %s\";\n", rc.Run.JobID, stepModel.ID, rc.Run.JobID, stepModel.ID)))
					if previousStep.If.Value != "" {
						f.Write([]byte(fmt.Sprintf("\"%s - cond %s\" -> \"%s - cond %s\";\n", rc.Run.JobID, previousStep.ID, rc.Run.JobID, stepModel.ID)))
						f.Write([]byte(fmt.Sprintf("\"%s - step %s\" -> \"%s - cond %s\";\n", rc.Run.JobID, previousStep.ID, rc.Run.JobID, stepModel.ID)))
					} else {
						f.Write([]byte(fmt.Sprintf("\"%s - step %s\" -> \"%s - cond %s\";\n", rc.Run.JobID, previousStep.ID, rc.Run.JobID, stepModel.ID)))
					}
				} else {
					// if strings.Contains(stepModel.If.Value, "failure") {
					// 	if previousStep.If.Value != "" {
					// 		f.Write([]byte(fmt.Sprintf("\"%s - cond %s\" -> \"%s - step %s\";\n", rc.Run.JobID, previousStep.Prev.ID, rc.Run.JobID, previousStep.ID)))
					// 	}
					// 	f.Write([]byte(fmt.Sprintf("\"%s - step %s\" -> \"%s - step %s\";\n", rc.Run.JobID, previousStep.Prev.ID, rc.Run.JobID, previousStep.ID)))
					// }
					if previousStep.If.Value != "" {
						f.Write([]byte(fmt.Sprintf("\"%s - cond %s\" -> \"%s - step %s\";\n", rc.Run.JobID, previousStep.ID, rc.Run.JobID, stepModel.ID)))
					}
					f.Write([]byte(fmt.Sprintf("\"%s - step %s\" -> \"%s - step %s\";\n", rc.Run.JobID, previousStep.ID, rc.Run.JobID, stepModel.ID)))
				}
			} else {
				if stepModel.If.Value != "" {
					if rc.Run.Job().If.Value != "" && !rc.Run.Job().CondInserted {
						f.Write([]byte(fmt.Sprintf("\"cond - %s\" -> \"%s - cond %s\";\n", rc.Run.JobID, rc.Run.JobID, stepModel.ID)))
					} else {
						f.Write([]byte(fmt.Sprintf("\"job - %s\" -> \"%s - cond %s\";\n", rc.Run.JobID, rc.Run.JobID, stepModel.ID)))
					}
					f.Write([]byte(fmt.Sprintf("\"%s - cond %s\" -> \"%s - step %s\";\n", rc.Run.JobID, stepModel.ID, rc.Run.JobID, stepModel.ID)))
				} else {
					f.Write([]byte(fmt.Sprintf("\"job - %s\" -> \"%s - step %s\";\n", rc.Run.JobID, rc.Run.JobID, stepModel.ID)))

				}
			}
		}
		//if step.getStepModel().If.Value != "" {
		//	log.Infof("\"%s - cond %s\"", step.getRunContext().Run.JobID, step.getStepModel().ID)
		//	log.Infof("\"%s - cond %s\" -> \"%s - step %s\"", step.getRunContext().Run.JobID, step.getStepModel().ID, step.getRunContext().Run.JobID, step.getStepModel().ID)
		//	f.Write([]byte(fmt.Sprintf("\"%s - step %s\";\n", step.getRunContext().Run.JobID, step.getStepModel().ID)))
		//	f.Write([]byte(fmt.Sprintf("\"%s - cond %s\" -> \"%s - step %s\";\n", step.getRunContext().Run.JobID, step.getStepModel().ID, step.getRunContext().Run.JobID, step.getStepModel().ID)))
		//}

		if errFile != nil {
			fmt.Errorf("  \u274C  Error in writing job condition to file \"if: %s\" (%s)", step.getStepModel().If, errFile)
		}
		if err != nil {
			stepResult.Conclusion = model.StepStatusFailure
			stepResult.Outcome = model.StepStatusFailure
			//f.Write([]byte(fmt.Sprintf("\"%s - step %s\" [color=\"red\"];\n", rc.Run.JobID, stepModel.ID)))
			//f.Write([]byte(fmt.Sprintf("; failed\n")))
			return err
		}

		if !runStep {
			stepResult.Conclusion = model.StepStatusSkipped
			stepResult.Outcome = model.StepStatusSkipped
			logger.WithField("stepResult", stepResult.Outcome).Debugf("Skipping step '%s' due to '%s'", stepModel, ifExpression)
			//f.Write([]byte(fmt.Sprintf("\"%s - step %s\" [color=\"red\"];\n", rc.Run.JobID, stepModel.ID)))
			//f.Write([]byte(fmt.Sprintf("; failed\n")))
			return nil
		}
		//f.Write([]byte(fmt.Sprintf("; achieved\n")))
		stepString := rc.ExprEval.Interpolate(ctx, stepModel.String())
		if strings.Contains(stepString, "::add-mask::") {
			stepString = "add-mask command"
		}
		logger.Infof("\u2B50 Run %s %s", stage, stepString)

		// Prepare and clean Runner File Commands
		actPath := rc.JobContainer.GetActPath()

		outputFileCommand := path.Join("workflow", "outputcmd.txt")
		(*step.getEnv())["GITHUB_OUTPUT"] = path.Join(actPath, outputFileCommand)

		stateFileCommand := path.Join("workflow", "statecmd.txt")
		(*step.getEnv())["GITHUB_STATE"] = path.Join(actPath, stateFileCommand)

		pathFileCommand := path.Join("workflow", "pathcmd.txt")
		(*step.getEnv())["GITHUB_PATH"] = path.Join(actPath, pathFileCommand)

		envFileCommand := path.Join("workflow", "envs.txt")
		(*step.getEnv())["GITHUB_ENV"] = path.Join(actPath, envFileCommand)

		summaryFileCommand := path.Join("workflow", "SUMMARY.md")
		(*step.getEnv())["GITHUB_STEP_SUMMARY"] = path.Join(actPath, summaryFileCommand)

		_ = rc.JobContainer.Copy(actPath, &container.FileEntry{
			Name: outputFileCommand,
			Mode: 0o666,
		}, &container.FileEntry{
			Name: stateFileCommand,
			Mode: 0o666,
		}, &container.FileEntry{
			Name: pathFileCommand,
			Mode: 0o666,
		}, &container.FileEntry{
			Name: envFileCommand,
			Mode: 0666,
		}, &container.FileEntry{
			Name: summaryFileCommand,
			Mode: 0o666,
		})(ctx)

		timeoutctx, cancelTimeOut := evaluateStepTimeout(ctx, rc.ExprEval, stepModel)
		defer cancelTimeOut()
		err = executor(timeoutctx)

		if err == nil {
			logger.WithField("stepResult", stepResult.Outcome).Infof("  \u2705  Success - %s %s", stage, stepString)
		} else {
			stepResult.Outcome = model.StepStatusFailure

			continueOnError, parseErr := isContinueOnError(ctx, stepModel.RawContinueOnError, step, stage)
			if parseErr != nil {
				stepResult.Conclusion = model.StepStatusFailure
				return parseErr
			}

			if continueOnError {
				logger.Infof("Failed but continue next step")
				err = nil
				stepResult.Conclusion = model.StepStatusSuccess
			} else {
				stepResult.Conclusion = model.StepStatusFailure
			}

			logger.WithField("stepResult", stepResult.Outcome).Errorf("  \u274C  Failure - %s %s", stage, stepString)
		}
		// Process Runner File Commands
		orgerr := err
		err = processRunnerEnvFileCommand(ctx, envFileCommand, rc, rc.setEnv)
		if err != nil {
			return err
		}
		err = processRunnerEnvFileCommand(ctx, stateFileCommand, rc, rc.saveState)
		if err != nil {
			return err
		}
		err = processRunnerEnvFileCommand(ctx, outputFileCommand, rc, rc.setOutput)
		if err != nil {
			return err
		}
		err = rc.UpdateExtraPath(ctx, path.Join(actPath, pathFileCommand))
		if err != nil {
			return err
		}
		if orgerr != nil {
			return orgerr
		}
		return err
	}
}

func evaluateStepTimeout(ctx context.Context, exprEval ExpressionEvaluator, stepModel *model.Step) (context.Context, context.CancelFunc) {
	timeout := exprEval.Interpolate(ctx, stepModel.TimeoutMinutes)
	if timeout != "" {
		if timeOutMinutes, err := strconv.ParseInt(timeout, 10, 64); err == nil {
			return context.WithTimeout(ctx, time.Duration(timeOutMinutes)*time.Minute)
		}
	}
	return ctx, func() {}
}

func setupEnv(ctx context.Context, step step) error {
	rc := step.getRunContext()

	mergeEnv(ctx, step)
	// merge step env last, since it should not be overwritten
	mergeIntoMap(step, step.getEnv(), step.getStepModel().GetEnv())

	exprEval := rc.NewExpressionEvaluator(ctx)
	for k, v := range *step.getEnv() {
		if !strings.HasPrefix(k, "INPUT_") {
			(*step.getEnv())[k] = exprEval.Interpolate(ctx, v)
		}
	}
	// after we have an evaluated step context, update the expressions evaluator with a new env context
	// you can use step level env in the with property of a uses construct
	exprEval = rc.NewExpressionEvaluatorWithEnv(ctx, *step.getEnv())
	for k, v := range *step.getEnv() {
		if strings.HasPrefix(k, "INPUT_") {
			(*step.getEnv())[k] = exprEval.Interpolate(ctx, v)
		}
	}

	common.Logger(ctx).Debugf("setupEnv => %v", *step.getEnv())

	return nil
}

func mergeEnv(ctx context.Context, step step) {
	env := step.getEnv()
	rc := step.getRunContext()
	job := rc.Run.Job()

	c := job.Container()
	if c != nil {
		mergeIntoMap(step, env, rc.GetEnv(), c.Env)
	} else {
		mergeIntoMap(step, env, rc.GetEnv())
	}

	rc.withGithubEnv(ctx, step.getGithubContext(ctx), *env)
}

func isStepEnabled(ctx context.Context, expr string, step step, stage stepStage) (bool, error) {
	rc := step.getRunContext()

	var defaultStatusCheck exprparser.DefaultStatusCheck
	if stage == stepStagePost {
		defaultStatusCheck = exprparser.DefaultStatusCheckAlways
	} else {
		defaultStatusCheck = exprparser.DefaultStatusCheckSuccess
	}

	runStep, err := EvalBool(ctx, rc.NewStepExpressionEvaluator(ctx, step), expr, defaultStatusCheck)
	if err != nil {

		return false, fmt.Errorf("  \u274C  Error in if-expression: \"if: %s\" (%s)", expr, err)
	}

	return runStep, nil
}

func isContinueOnError(ctx context.Context, expr string, step step, _ stepStage) (bool, error) {
	// https://github.com/github/docs/blob/3ae84420bd10997bb5f35f629ebb7160fe776eae/content/actions/reference/workflow-syntax-for-github-actions.md?plain=true#L962
	if len(strings.TrimSpace(expr)) == 0 {
		return false, nil
	}

	rc := step.getRunContext()

	continueOnError, err := EvalBool(ctx, rc.NewStepExpressionEvaluator(ctx, step), expr, exprparser.DefaultStatusCheckNone)
	if err != nil {
		return false, fmt.Errorf("  \u274C  Error in continue-on-error-expression: \"continue-on-error: %s\" (%s)", expr, err)
	}

	return continueOnError, nil
}

func mergeIntoMap(step step, target *map[string]string, maps ...map[string]string) {
	if rc := step.getRunContext(); rc != nil && rc.JobContainer != nil && rc.JobContainer.IsEnvironmentCaseInsensitive() {
		mergeIntoMapCaseInsensitive(*target, maps...)
	} else {
		mergeIntoMapCaseSensitive(*target, maps...)
	}
}

func mergeIntoMapCaseSensitive(target map[string]string, maps ...map[string]string) {
	for _, m := range maps {
		for k, v := range m {
			target[k] = v
		}
	}
}

func mergeIntoMapCaseInsensitive(target map[string]string, maps ...map[string]string) {
	foldKeys := make(map[string]string, len(target))
	for k := range target {
		foldKeys[strings.ToLower(k)] = k
	}
	toKey := func(s string) string {
		foldKey := strings.ToLower(s)
		if k, ok := foldKeys[foldKey]; ok {
			return k
		}
		foldKeys[strings.ToLower(foldKey)] = s
		return s
	}
	for _, m := range maps {
		for k, v := range m {
			target[toKey(k)] = v
		}
	}
}
