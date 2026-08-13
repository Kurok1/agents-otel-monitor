/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.0.0
 */

import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import App from './App'
import './index.css'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
