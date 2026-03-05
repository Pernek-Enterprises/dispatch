import { loadConfig } from './state.js';

/**
 * Send a prompt to a model endpoint (direct API call).
 * Used for quick LLM jobs: triage, parse, answer.
 * 
 * Model endpoint is resolved from models.json — never hardcoded.
 */
export async function callModel(modelId, prompt, { systemPrompt, maxTokens } = {}) {
  const models = loadConfig('models');
  const model = models[modelId];
  if (!model) {
    throw new Error(
      `Unknown model "${modelId}". Available models: ${Object.keys(models).join(', ')}. Check models.json.`
    );
  }

  if (!model.endpoint) {
    throw new Error(`Model "${modelId}" has no endpoint configured in models.json.`);
  }

  const messages = [];
  if (systemPrompt) {
    messages.push({ role: 'system', content: systemPrompt });
  }
  messages.push({ role: 'user', content: prompt });

  const response = await fetch(`${model.endpoint}/chat/completions`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      messages,
      max_tokens: maxTokens || 2048,
      temperature: 0.7
    })
  });

  if (!response.ok) {
    const text = await response.text();
    throw new Error(`Model ${modelId} (${model.endpoint}) error ${response.status}: ${text}`);
  }

  const data = await response.json();
  return data.choices?.[0]?.message?.content || '';
}

/**
 * Get the provider string for a model (used when spawning sessions).
 * Returns the "provider" field from models.json.
 */
export function getModelProvider(modelId) {
  const models = loadConfig('models');
  const model = models[modelId];
  if (!model) return null;
  return model.provider || null;
}
