import { Navigate, Route, Routes } from 'react-router-dom';
import { AuthProvider } from './auth/AuthProvider';
import { RedirectIfAuthed, RequireAdmin, RequireAuth } from './auth/guards';
import { Layout } from './components/Layout';
import { ToastProvider } from './components/ui/Toast';
import { AlertGroupDetailPage } from './pages/AlertGroupDetailPage';
import { AlertsGroupsPage } from './pages/AlertsGroupsPage';
import { AlertsIndexPage } from './pages/AlertsIndexPage';
import { DashboardPage } from './pages/DashboardPage';
import { LoginPage } from './pages/LoginPage';
import { MailboxEditPage } from './pages/MailboxEditPage';
import { MailDetailPage } from './pages/MailDetailPage';
import { MailsIndexPage } from './pages/MailsIndexPage';
import { MailsListPage } from './pages/MailsListPage';
import { NotFoundPage } from './pages/NotFoundPage';
import { SettingsGeneralPage } from './pages/SettingsGeneralPage';
import { SettingsMailboxesPage } from './pages/SettingsMailboxesPage';
import { SettingsMenuPage } from './pages/SettingsMenuPage';
import { SettingsNotificationsPage } from './pages/SettingsNotificationsPage';
import { SettingsProfilePage } from './pages/SettingsProfilePage';
import { SettingsTokensPage } from './pages/SettingsTokensPage';
import { SettingsUsersPage } from './pages/SettingsUsersPage';
import { SetupPage } from './pages/SetupPage';
import { ToolsPage } from './pages/ToolsPage';

// Client-side routes. The server falls back to index.html for any path it
// does not know, so every route here also works on a full page reload.
export default function App() {
    return (
        <ToastProvider>
            <AuthProvider>
                <Routes>
                    <Route
                        path='/setup'
                        element={
                            <RedirectIfAuthed>
                                <SetupPage />
                            </RedirectIfAuthed>
                        }
                    />
                    <Route
                        path='/login'
                        element={
                            <RedirectIfAuthed>
                                <LoginPage />
                            </RedirectIfAuthed>
                        }
                    />
                    <Route
                        element={
                            <RequireAuth>
                                <Layout />
                            </RequireAuth>
                        }
                    >
                        <Route path='/' element={<DashboardPage />} />
                        <Route path='/alerts' element={<AlertsIndexPage />} />
                        <Route path='/alerts/:mailboxId' element={<AlertsGroupsPage />} />
                        <Route path='/alerts/:mailboxId/groups/:groupKey' element={<AlertGroupDetailPage />} />
                        <Route path='/mails' element={<MailsIndexPage />} />
                        <Route path='/mails/:mailboxId' element={<MailsListPage />} />
                        <Route path='/mails/:mailboxId/:messageKey' element={<MailDetailPage />} />
                        <Route path='/tools' element={<ToolsPage />} />
                        <Route
                            path='/settings'
                            element={
                                <RequireAdmin>
                                    <SettingsMenuPage />
                                </RequireAdmin>
                            }
                        />
                        <Route
                            path='/settings/general'
                            element={
                                <RequireAdmin>
                                    <SettingsGeneralPage />
                                </RequireAdmin>
                            }
                        />
                        <Route
                            path='/settings/notifications'
                            element={
                                <RequireAdmin>
                                    <SettingsNotificationsPage />
                                </RequireAdmin>
                            }
                        />
                        <Route
                            path='/settings/mailboxes'
                            element={
                                <RequireAdmin>
                                    <SettingsMailboxesPage />
                                </RequireAdmin>
                            }
                        />
                        <Route
                            path='/settings/mailboxes/new'
                            element={
                                <RequireAdmin>
                                    <MailboxEditPage />
                                </RequireAdmin>
                            }
                        />
                        <Route
                            path='/settings/mailboxes/:id'
                            element={
                                <RequireAdmin>
                                    <MailboxEditPage />
                                </RequireAdmin>
                            }
                        />
                        <Route
                            path='/settings/users'
                            element={
                                <RequireAdmin>
                                    <SettingsUsersPage />
                                </RequireAdmin>
                            }
                        />
                        <Route
                            path='/settings/tokens'
                            element={
                                <RequireAdmin>
                                    <SettingsTokensPage />
                                </RequireAdmin>
                            }
                        />
                        <Route path='/settings/profile' element={<SettingsProfilePage />} />
                        <Route path='/settings/account' element={<Navigate to='/settings/profile' replace />} />
                        <Route path='/index.html' element={<Navigate to='/' replace />} />
                        <Route path='*' element={<NotFoundPage />} />
                    </Route>
                </Routes>
            </AuthProvider>
        </ToastProvider>
    );
}
