package bot

import (
	"context"
	"strings"
	"testing"

	"github.com/runixer/laplaced/internal/artifactdelivery"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func plannedLoaded(id int64, mime, name string, data []byte, mode artifactdelivery.Mode, source string) loadedArtifactDelivery {
	return loadedArtifactDelivery{
		loadedArtifact: loadedArtifact{
			artifact: &storage.Artifact{ID: id, UserID: "scope", MimeType: mime, OriginalName: name},
			data:     append([]byte(nil), data...),
			ordinal:  1,
		},
		mode: mode, source: source,
	}
}

func TestBuildArtifactResponsePlanGeneratedModesIgnoreSizeThreshold(t *testing.T) {
	bot, path := generatedV2Planner(t)
	bot.cfg.Agents.ImageGenerator.DocumentThresholdBytes = 1

	t.Run("preview only", func(t *testing.T) {
		generated := []loadedArtifactDelivery{plannedLoaded(1, "image/png", "large.png", generatedTestPNG,
			artifactdelivery.ModePreview, artifactReferenceSourceGenerated)}
		planned, err := bot.buildArtifactResponsePlan(context.Background(), path,
			"Before.\n\n###MEDIA:1###\n\nAfter.", generated, nil)
		require.NoError(t, err)
		require.Len(t, planned.plan.Operations, 1)
		require.NotNil(t, planned.plan.Operations[0].RichMedia)
		assert.Equal(t, OutgoingMediaWireKindPhoto, planned.plan.Operations[0].RichMedia.Items[0].WireKind)
		assert.Zero(t, planned.generatedOriginals)
	})

	t.Run("original replaces preview", func(t *testing.T) {
		generated := []loadedArtifactDelivery{plannedLoaded(1, "image/png", "large.png", generatedTestPNG,
			artifactdelivery.ModeOriginal, artifactReferenceSourceGenerated)}
		planned, err := bot.buildArtifactResponsePlan(context.Background(), path, "File only.", generated, nil)
		require.NoError(t, err)
		require.Len(t, planned.plan.Operations, 2)
		assert.Equal(t, persistentOperationRichText, planned.plan.Operations[0].Kind)
		assert.Equal(t, persistentOperationMedia, planned.plan.Operations[1].Kind)
		assert.Equal(t, OutgoingMediaWireKindDocument, planned.plan.Operations[1].Media.Items[0].WireKind)
		assert.Equal(t, "generated-image-1.png", planned.plan.Operations[1].Media.Items[0].Filename)
		assert.Equal(t, 1, planned.generatedOriginals)
	})
}

func TestBuildArtifactResponsePlanBothPutsOriginalsAfterWholeRichPostAndFallback(t *testing.T) {
	bot, path := generatedV2Planner(t)
	generated := []loadedArtifactDelivery{plannedLoaded(1, "image/png", "source.png", generatedTestPNG,
		artifactdelivery.ModePreviewAndOriginal, artifactReferenceSourceGenerated)}
	planned, err := bot.buildArtifactResponsePlan(context.Background(), path,
		"# One\n\n###MEDIA:1###\n\n###SPLIT###\n\n# Two", generated, nil)
	require.NoError(t, err)
	require.Len(t, planned.plan.Operations, 3)
	assert.Equal(t, persistentOperationRichMedia, planned.plan.Operations[0].Kind)
	assert.Equal(t, persistentOperationRichText, planned.plan.Operations[1].Kind)
	assert.Equal(t, persistentOperationMedia, planned.plan.Operations[2].Kind)
	assert.Equal(t, OutgoingMediaWireKindDocument, planned.plan.Operations[2].Media.Items[0].WireKind)
	for _, operation := range planned.plan.Operations[:2] {
		require.NotEmpty(t, operation.formatFallback)
		last := operation.formatFallback[len(operation.formatFallback)-1]
		require.NotNil(t, last.Media)
		assert.Equal(t, OutgoingMediaWireKindDocument, last.Media.Items[0].WireKind)
	}
	assert.Equal(t, []plannedArtifactReference{
		{artifactID: 1, mode: artifactdelivery.ModePreview, source: artifactReferenceSourceGenerated},
		{artifactID: 1, mode: artifactdelivery.ModeOriginal, source: artifactReferenceSourceGenerated},
	}, planned.references)
}

func TestBuildArtifactResponsePlanReferencesFollowPersistentPresentationOrder(t *testing.T) {
	bot, path := generatedV2Planner(t)
	generated := []loadedArtifactDelivery{
		plannedLoaded(1, "image/png", "one.png", generatedTestPNG,
			artifactdelivery.ModePreviewAndOriginal, artifactReferenceSourceGenerated),
		plannedLoaded(2, "image/png", "two.png", generatedTestPNG,
			artifactdelivery.ModePreview, artifactReferenceSourceGenerated),
	}
	planned, err := bot.buildArtifactResponsePlan(context.Background(), path,
		"###MEDIA:2###\n\n###SPLIT###\n\n###MEDIA:1###", generated, nil)
	require.NoError(t, err)
	require.Len(t, planned.references, 3)
	assert.Equal(t, []plannedArtifactReference{
		{artifactID: 2, mode: artifactdelivery.ModePreview, source: artifactReferenceSourceGenerated},
		{artifactID: 1, mode: artifactdelivery.ModePreview, source: artifactReferenceSourceGenerated},
		{artifactID: 1, mode: artifactdelivery.ModeOriginal, source: artifactReferenceSourceGenerated},
	}, planned.references)
	require.Len(t, planned.loaded, 2)
	assert.Equal(t, []int64{2, 1}, []int64{planned.loaded[0].artifact.ID, planned.loaded[1].artifact.ID})
}

func TestBuildArtifactResponsePlanStoredSelectionPreservesItemAndPresentationOrder(t *testing.T) {
	bot, path := generatedV2Planner(t)
	selected := []loadedArtifactDelivery{
		plannedLoaded(10, "image/png", "one.png", generatedTestPNG, artifactdelivery.ModePreviewAndOriginal, artifactReferenceSourceStored),
		plannedLoaded(20, "application/pdf", "report.pdf", []byte("pdf"), artifactdelivery.ModeOriginal, artifactReferenceSourceStored),
		plannedLoaded(30, "image/png", "two.png", generatedTestPNG, artifactdelivery.ModePreview, artifactReferenceSourceStored),
	}
	planned, err := bot.buildArtifactResponsePlan(context.Background(), path, "Here they are.", nil, selected)
	require.NoError(t, err)
	require.Len(t, planned.plan.Operations, 4)
	assert.Equal(t, persistentOperationRichText, planned.plan.Operations[0].Kind)
	require.Equal(t, OutgoingMediaWireKindPhoto, planned.plan.Operations[1].Media.Items[0].WireKind)
	require.Len(t, planned.plan.Operations[2].Media.Items, 2, "contiguous originals share one document album")
	assert.Equal(t, []string{"one.png", "report.pdf"}, []string{
		planned.plan.Operations[2].Media.Items[0].Filename,
		planned.plan.Operations[2].Media.Items[1].Filename,
	})
	require.Equal(t, OutgoingMediaWireKindDocument, planned.plan.Operations[2].Media.Items[0].WireKind)
	require.Equal(t, OutgoingMediaWireKindPhoto, planned.plan.Operations[3].Media.Items[0].WireKind)
	assert.Equal(t, []int64{10, 10, 20, 30}, []int64{
		planned.references[0].artifactID,
		planned.references[1].artifactID,
		planned.references[2].artifactID,
		planned.references[3].artifactID,
	})
}

func TestBuildArtifactResponsePlanUnpreviewableImageDegradesOnceToDocument(t *testing.T) {
	bot, path := generatedV2Planner(t)
	generated := []loadedArtifactDelivery{plannedLoaded(1, "image/png", "broken.png", []byte("not an image"),
		artifactdelivery.ModePreviewAndOriginal, artifactReferenceSourceGenerated)}
	planned, err := bot.buildArtifactResponsePlan(context.Background(), path, "###MEDIA:1###", generated, nil)
	require.NoError(t, err)
	require.Len(t, planned.plan.Operations, 1)
	require.NotNil(t, planned.plan.Operations[0].Media)
	assert.Len(t, planned.plan.Operations[0].Media.Items, 1)
	assert.Equal(t, OutgoingMediaWireKindDocument, planned.plan.Operations[0].Media.Items[0].WireKind)
	assert.NotContains(t, planned.historyText, "###MEDIA")
}

func TestBuildArtifactResponsePlanMIMEAndImageContainerMustAgreeForPreview(t *testing.T) {
	bot, path := generatedV2Planner(t)
	selected := []loadedArtifactDelivery{plannedLoaded(1, "image/jpeg", "mismatch.jpg", generatedTestPNG,
		artifactdelivery.ModeAuto, artifactReferenceSourceStored)}
	planned, err := bot.buildArtifactResponsePlan(context.Background(), path, "Stored image.", nil, selected)
	require.NoError(t, err)
	require.Len(t, planned.plan.Operations, 2)
	require.NotNil(t, planned.plan.Operations[1].Media)
	assert.Equal(t, OutgoingMediaWireKindDocument, planned.plan.Operations[1].Media.Items[0].WireKind)
	assert.Equal(t, artifactdelivery.ModeOriginal, planned.references[0].mode)
}

func TestBuildArtifactResponsePlanKeepsDuplicateGeneratedOutputSlots(t *testing.T) {
	bot, path := generatedV2Planner(t)
	generated := []loadedArtifactDelivery{
		plannedLoaded(1, "image/png", "same.png", generatedTestPNG,
			artifactdelivery.ModePreview, artifactReferenceSourceGenerated),
		plannedLoaded(1, "image/png", "same.png", generatedTestPNG,
			artifactdelivery.ModePreview, artifactReferenceSourceGenerated),
	}
	planned, err := bot.buildArtifactResponsePlan(context.Background(), path, "###MEDIA:1,2###", generated, nil)
	require.NoError(t, err)
	require.Len(t, planned.plan.Operations, 1)
	assert.Len(t, planned.plan.Operations[0].RichMedia.Items, 2)
	assert.Equal(t, []int64{1, 1}, []int64{planned.references[0].artifactID, planned.references[1].artifactID})
	assert.Equal(t, []int64{1}, planned.ownedArtifactIDs)
	assert.Len(t, planned.loaded, 1, "history marker inventory is deduplicated even when output slots repeat")
}

func TestSafeArtifactFilenameUsesBasenameControlsAndBoundedFallback(t *testing.T) {
	assert.Equal(t, "report.pdf", safeArtifactFilename(`../private\\report.pdf`, 7, "application/pdf"))
	assert.Equal(t, "badname.png", safeArtifactFilename("bad\r\nname.png", 8, "image/png"))
	assert.Equal(t, "attachment-9.pdf", safeArtifactFilename("..", 9, "application/pdf"))
	assert.LessOrEqual(t, len([]rune(safeArtifactFilename(strings.Repeat("д", 240)+".png", 10, "image/png"))), 180)
}
