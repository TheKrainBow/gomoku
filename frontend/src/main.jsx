import React from 'react'
import { createRoot } from 'react-dom/client'
import App from './App.jsx'
import AnalysePage from './AnalysePage.jsx'
import './App.css'

const path = window.location.pathname
const Page = path === '/analyse' ? AnalysePage : App

createRoot(document.getElementById('root')).render(<Page />)
