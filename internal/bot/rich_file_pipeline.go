package bot

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/sync/errgroup"

	"github.com/runixer/laplaced/internal/files"
	"github.com/runixer/laplaced/internal/storage"
)

var (
	errRichMessageUnsupported = errors.New("incoming rich message has no usable content")
	errRichMediaUnavailable   = errors.New("incoming rich message media is unavailable")
)

type groupedFileResults struct {
	processed [][]*files.ProcessedFile
	issues    [][]files.ProcessFileResult
}

// processGroupedFiles keeps the established single-file legacy behavior while
// processing all files belonging to rich messages under one aggregate budget.
// The latter is deliberately grouped across the whole turn: otherwise N rich
// messages merged by MessageGrouper would each receive a fresh 20 MiB budget.
func (b *Bot) processGroupedFiles(
	ctx context.Context,
	messages []IncomingMessage,
	userID storage.ScopeID,
	groupText string,
) (groupedFileResults, error) {
	result := groupedFileResults{
		processed: make([][]*files.ProcessedFile, len(messages)),
		issues:    make([][]files.ProcessFileResult, len(messages)),
	}

	type richRef struct {
		messageIndex int
		file         files.IncomingFile
	}
	var richFiles []richRef

	g, gCtx := errgroup.WithContext(ctx)
	for i := range messages {
		i := i
		message := messages[i]
		if message.Ingress != nil && message.Ingress.Kind == "rich" {
			for _, file := range message.Files {
				richFiles = append(richFiles, richRef{messageIndex: i, file: file})
			}
			continue
		}

		g.Go(func() error {
			processed, err := b.fileProcessor.ProcessFiles(gCtx, message.Files, userID, groupText)
			if err != nil {
				return &fileProcessingError{err: err, messageIndex: i}
			}
			result.processed[i] = processed
			return nil
		})
	}

	var detailed []files.ProcessFileResult
	if len(richFiles) > 0 {
		incoming := make([]files.IncomingFile, len(richFiles))
		for i := range richFiles {
			incoming[i] = richFiles[i].file
		}
		g.Go(func() error {
			detailed = b.fileProcessor.ProcessFilesDetailed(gCtx, incoming, userID, groupText)
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return groupedFileResults{}, err
	}
	if err := ctx.Err(); err != nil {
		return groupedFileResults{}, err
	}

	for i := range detailed {
		messageIndex := richFiles[i].messageIndex
		result.issues[messageIndex] = append(result.issues[messageIndex], detailed[i])
		if detailed[i].Status == files.ProcessFileAvailable && detailed[i].Processed != nil {
			result.processed[messageIndex] = append(result.processed[messageIndex], detailed[i].Processed)
		}
	}

	return result, nil
}

func richFileIssueMarker(result files.ProcessFileResult) string {
	ordinal := result.Incoming.Ordinal
	if ordinal <= 0 {
		ordinal = 1
	}
	return fmt.Sprintf(
		"[Telegram rich media #%d (%s, %s): %s]",
		ordinal,
		result.Incoming.Kind,
		result.Incoming.BlockPath,
		result.Status,
	)
}
