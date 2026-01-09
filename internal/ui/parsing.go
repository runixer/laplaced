package ui

// This file previously contained parsing functions for RAGLog, RerankerLog, and TopicLog.
// These have been replaced by the unified agent_logs table and the agent_log.html template.
// The new UI displays raw InputPrompt, InputContext, OutputResponse, and OutputParsed
// fields directly, using the prettyJSON template function for formatting.
