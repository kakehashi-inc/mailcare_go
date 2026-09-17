import { Route, Routes } from 'react-router-dom';
import { Header } from './components/Header';
import { HomePage } from './components/HomePage';
import { NotFoundPage } from './components/NotFoundPage';

// Client-side routes. The server falls back to index.html for any path it
// does not know, so every route here also works on a full page reload.
export default function App() {
    return (
        <div className='flex min-h-screen flex-col'>
            <Header />
            <main className='flex flex-1 flex-col'>
                <Routes>
                    <Route path='/' element={<HomePage />} />
                    <Route path='*' element={<NotFoundPage />} />
                </Routes>
            </main>
        </div>
    );
}
