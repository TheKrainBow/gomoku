import React from 'react'
import { createRoot } from 'react-dom/client'
import App from './App.jsx'
import CachePage from './CachePage.jsx'
import MinmaxPage from './MinmaxPage.jsx'
import TrainerPage from './TrainerPage.jsx'
import './App.css'

const path = window.location.pathname
const Page = path === '/cache' ? CachePage : path === '/minmax' ? MinmaxPage : path === '/trainer' ? TrainerPage : App

createRoot(document.getElementById('root')).render(<Page />)
