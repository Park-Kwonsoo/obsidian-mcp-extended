/**
 * Formats tool responses according to MCP specification
 */

/**
 * Creates a text response
 * @param {string} text - The text content
 * @param {object} [metadata] - Optional execution metadata
 * @returns {object} MCP-compliant response
 */
export function textResponse(text, metadata = null) {
  const response = {
    content: [
      {
        type: 'text',
        text
      }
    ]
  };
  
  if (metadata) {
    response._meta = metadata;
  }
  
  return response;
}

/**
 * Creates a structured response with both text and structured content
 * @param {object} data - The structured data
 * @param {string} [description] - Optional text description
 * @param {object} [metadata] - Optional execution metadata
 * @returns {object} MCP-compliant response
 */
export function structuredResponse(data, description = null, metadata = null) {
  const response = {
    content: [
      {
        type: 'text',
        text: description || JSON.stringify(data, null, 2)
      }
    ],
    structuredContent: data
  };
  
  if (metadata) {
    response._meta = metadata;
  }
  
  return response;
}

/**
 * Creates an error response
 * @param {Error|MCPError} error - The error to format
 * @returns {object} MCP-compliant error response
 */
export function errorResponse(error) {
  return {
    content: [
      {
        type: 'text',
        text: `Error: ${error.message}`
      }
    ],
    isError: true
  };
}

/**
 * Creates execution metadata for responses
 * @param {number} startTime - The start time from Date.now()
 * @param {object} [additional] - Additional metadata fields
 * @returns {object} Metadata object
 */
export function createMetadata(startTime, additional = {}) {
  return {
    executionTime: Date.now() - startTime,
    timestamp: new Date().toISOString(),
    ...additional
  };
}

/**
 * Appends a bulleted preview of items to an existing description.
 * Truncates at `maxPreview` and appends a `... and N more` tail so `content[].text`
 * stays useful for LLM clients without exploding token usage when a caller
 * requests a large page. Full data still travels via structuredContent.
 * @param {string} description - Existing description
 * @param {Array} items - Items to render; no-op when falsy or empty
 * @param {(item: any) => string} formatter - Per-item renderer
 * @param {number} [maxPreview=50] - Max items shown before truncation tail
 * @returns {string} New description
 */
export function appendItemList(description, items, formatter, maxPreview = 50) {
  if (!items || items.length === 0) return description;
  const shown = items.slice(0, maxPreview).map(formatter).join('\n');
  const tail = items.length > maxPreview
    ? `\n... and ${items.length - maxPreview} more`
    : '';
  return description + '\n\n' + shown + tail;
}

/**
 * Strips verbose context data from search results to reduce token usage
 * Removes context.lines arrays while keeping essential match information
 * @param {object} searchResults - The search results object
 * @returns {object} Search results with context stripped
 */
export function stripSearchContext(searchResults) {
  if (!searchResults || !searchResults.files) {
    return searchResults;
  }

  return {
    ...searchResults,
    files: searchResults.files.map(file => ({
      ...file,
      matches: file.matches.map(match => {
        // Remove context.lines array but keep highlighted snippet
        if (match.context) {
          return {
            line: match.line,
            content: match.content,
            context: {
              highlighted: match.context.highlighted
            }
          };
        }
        return match;
      })
    }))
  };
}