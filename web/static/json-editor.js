import { JSONEditor } from '/static/vanilla-jsoneditor/standalone.js'

let content = {
  text: undefined,
  json: {
      greeting: 'Hello World'
  }
}

const editor = new JSONEditor({
  target: document.getElementById('jsoneditor'),
  props: {
    content,
    onChange: (updatedContent, previousContent, { contentErrors, patchResult }) => {
        // content is an object { json: JSONData } | { text: string }
        console.log('onChange', { updatedContent, previousContent, contentErrors, patchResult })
        content = updatedContent
    },
    mode: 'text',
    mainMenuBar: false,
  }
})