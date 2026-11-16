import React from 'react'

const PilotSozlesmesi = () => {
    return (
        <div className="min-h-screen bg-background flex flex-col justify-center py-12 sm:px-6 lg:px-8 font-sans">
            <div className="sm:mx-auto sm:w-full sm:max-w-4xl">
                <h2 className="mt-6 text-center text-3xl text-text font-serif">
                    Pilot Kullanıcı Sözleşmesi
                </h2>
                <div className="mt-8 bg-surface py-8 px-4 shadow-subtle sm:rounded-lg sm:px-10">
                    <div className="max-w-none text-text-secondary">
                        <p className="text-sm text-text-tertiary mb-4 font-sans">Son Güncelleme: 12 Mayıs 2026 · Sürüm: 1.0</p>

                        <div className="mb-6 border-l-4 border-accent pl-4 py-3 text-text-secondary font-sans text-sm bg-background/40">
                            <strong className="text-text font-medium block mb-1">Bu Belge Hakkında</strong>
                            Bu Sözleşme, Syncrun'ın kapalı beta / PoC programına davet edilen pilot kullanıcılar için <a href="/terms-of-service" className="text-accent hover:text-accent-hover">Kullanım Koşulları</a>, <a href="/privacy-policy" className="text-accent hover:text-accent-hover">Gizlilik Politikası</a> ve <a href="/kvkk" className="text-accent hover:text-accent-hover">KVKK Aydınlatma Metni</a>'ne <strong>ek olarak</strong> imzalanır. Genel Kullanım Koşulları'nın aksine, bu Sözleşme bireysel onayla yürürlüğe girer.
                        </div>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">1. Taraflar</h3>
                        <p className="font-sans">
                            Bu Pilot Kullanıcı Sözleşmesi ("Sözleşme"), bir tarafta Syncrun adı altında pilot hizmet sunan eşit oranda ortak veri sorumluları <strong>Faik İhsan Südüpak</strong> ve <strong>Kerem Yoldaş</strong> (birlikte "Syncrun") ile diğer tarafta pilot programa davet edilmiş gerçek veya tüzel kişi ("Pilot Kullanıcı", "siz") arasında akdedilmiştir.
                        </p>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">2. Pilot Programın Niteliği</h3>
                        <ul className="list-disc pl-5 mt-2 space-y-1 font-sans">
                            <li>Syncrun şu anda <strong>Proof of Concept (PoC) / kapalı beta</strong> aşamasındadır.</li>
                            <li>Hizmet, kararlı (production) sürüm öncesidir; özellikler eksik, hatalı veya zaman içinde değişebilir niteliktedir.</li>
                            <li>Yapay zekâ ile üretilen transkript, özet, karar ve aksiyon çıkarımları <strong>hatalı, eksik veya yanıltıcı</strong> olabilir.</li>
                            <li>Pilot Kullanıcı, Hizmet'in "olduğu gibi" (AS-IS) sunulduğunu, hizmet seviyesi (SLA) ve kesintisizlik garantisi bulunmadığını kabul eder.</li>
                            <li>Pilot Kullanıcı, Hizmet çıktılarını mali, hukuki, tıbbi veya kritik operasyonel kararlarda <strong>doğrulamadan kullanmayacağını</strong> taahhüt eder.</li>
                        </ul>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">3. Süre ve Sona Erme</h3>
                        <p className="font-sans">
                            Bu Sözleşme, Pilot Kullanıcı'nın elektronik onay verdiği tarihte yürürlüğe girer ve pilot programın sona erdiği tarihte veya taraflardan birinin fesih bildirimine kadar geçerlidir. <strong>Her iki taraf da, herhangi bir gerekçe göstermeksizin, e-posta yolu ile bildirimde bulunarak Sözleşmeyi derhal feshedebilir.</strong>
                        </p>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">4. Pilot Kullanıcı Yükümlülükleri</h3>
                        <p className="font-sans">Pilot Kullanıcı, aşağıdaki yükümlülükleri kabul ve taahhüt eder:</p>
                        <ol className="list-decimal pl-5 mt-2 space-y-2 font-sans">
                            <li>
                                <strong>Toplantı katılımcılarının yazılı bilgilendirilmesi ve delillendirme:</strong> Pilot Kullanıcı, her toplantı öncesinde tüm katılımcılara (a) toplantının kaydedileceğini, (b) yapay zekâ ile transkripsiyon, özetleme, karar ve aksiyon çıkarımı yapılacağını, (c) verilerin yurt dışındaki servis sağlayıcılara aktarılacağını <strong>yazılı olarak</strong> — tercihen toplantı davetinde, davet açıklamasında veya toplantı başlangıcında sohbet/e-posta/ekran paylaşımı yoluyla — bildirmekle yükümlüdür. Bildirim yapıldığını ve katılımcıların itiraz etmediğini ispatlamaya yarayan kayıtları (e-posta yazışmaları, davet ekran görüntüleri, toplantı başında yapılan açıklamanın ses kaydı vb.) makul bir süre saklamak da Pilot Kullanıcı'nın sorumluluğundadır.
                                <br /><br />
                                Bu yükümlülük münhasıran Pilot Kullanıcı'ya aittir. Bildirimin yapılmaması, eksik yapılması veya gerekli açık rızanın alınmamasından doğacak <strong>5237 sayılı Türk Ceza Kanunu m.134 (özel hayatın gizliliğini ihlal), KVKK ve GDPR dâhil olmak üzere</strong> her türlü hukuki, idari ve cezai sonuçtan yalnızca Pilot Kullanıcı sorumludur.
                                <br /><br />
                                <strong>Tazminat Üstlenimi (Indemnification):</strong> Pilot Kullanıcı, bu yükümlülüğün ihlali sebebiyle Syncrun ve/veya veri sorumluları aleyhine üçüncü kişilerce açılacak her türlü dava, savcılık soruşturması, idari para cezası veya tazminat talebi nedeniyle doğacak avukatlık ücreti, mahkeme gideri, idari para cezası ve tazminat tutarlarını ilk talepte ve gayrikabili rücu olarak Syncrun'a ödemeyi taahhüt eder.
                            </li>
                            <li>
                                <strong>Hassas alanlarda kullanmama:</strong> Sağlık (hekim-hasta), hukuk (avukat-müvekkil), psikolojik danışmanlık, mali müşavirlik ve devlet/gizli sınıflandırılmış görüşmelerde Hizmet'i kullanmayacağını kabul eder.
                            </li>
                            <li>
                                <strong>Geri bildirim:</strong> Karşılaşılan hata, eksiklik ve iyileştirme önerilerini makul bir süre içinde Syncrun ekibine bildirmeyi kabul eder. Geri bildirim, pilot programın temel karşılığıdır.
                            </li>
                            <li>
                                <strong>Üçüncü kişi içeriği:</strong> Telif, marka veya kişilik hakları başkalarına ait içerikleri Hizmet'e yüklememeyi taahhüt eder.
                            </li>
                            <li>
                                <strong>Güvenlik:</strong> Hesap erişim bilgilerini güvende tutmakla, paylaşmamakla ve şüpheli erişim durumunda Syncrun'a derhal haber vermekle yükümlüdür.
                            </li>
                        </ol>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">5. Veri Koruma ve Açık Rıza</h3>
                        <p className="font-sans">
                            Kişisel verilerinizin işlenmesi, aktarımı ve haklarınız <a href="/privacy-policy" className="text-accent hover:text-accent-hover">Gizlilik Politikası</a> ve <a href="/kvkk" className="text-accent hover:text-accent-hover">KVKK Aydınlatma Metni</a>'nde detaylı olarak açıklanmıştır. Bu Sözleşme'yi onaylayarak ayrıca aşağıdaki <strong>açık rıza beyanlarını</strong> da vermiş sayılırsınız:
                        </p>
                        <ul className="list-disc pl-5 mt-2 space-y-1 font-sans">
                            <li>Toplantı içeriklerinizin yapay zekâ tabanlı transkripsiyon, özetleme ve karar/aksiyon çıkarımı amacıyla işlenmesine,</li>
                            <li>Bu kapsamda kişisel verilerinizin yurt dışındaki hizmet sağlayıcılara (Supabase, OpenAI, ElevenLabs, Google, Slack, Microsoft Azure) aktarılmasına KVKK m.9 uyarınca açık rıza verdiğinize.</li>
                        </ul>
                        <p className="mt-2 font-sans">
                            Açık rızanızı her zaman support@syncrun.net adresine bildirimle geri çekebilirsiniz; bu durumda Hizmet'in temel işlevlerini kullanamayabilirsiniz.
                        </p>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">6. Gizlilik (Pilot Kullanıcı Tarafından Korunacak)</h3>
                        <p className="font-sans">
                            Pilot süresi boyunca Syncrun'a ait <strong>henüz kamuya açıklanmamış özellikler, ekran görüntüleri, fiyatlandırma planları, performans verileri, hata kayıtları ve teknik mimari bilgileri</strong> gizli bilgi niteliğindedir. Pilot Kullanıcı bu bilgileri yalnızca pilot kullanım amacıyla kullanır, üçüncü kişilere kamuya açıklanmadan paylaşmaz. Bu yükümlülük Sözleşmenin sona ermesinden sonra <strong>2 (iki) yıl</strong> süreyle devam eder.
                        </p>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">7. Ücret</h3>
                        <p className="font-sans">
                            Pilot programı süresince Hizmet <strong>ücretsiz</strong> olarak sunulur. Pilot süresinin sonunda Hizmet'in ücretli plana geçmesi hâlinde, Pilot Kullanıcı'ya en az 30 (otuz) gün önceden bildirim yapılır; bildirim sonrası kullanıma devam yeni koşulların kabulü anlamına gelir.
                        </p>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">8. Sorumluluğun Sınırlandırılması</h3>
                        <p className="font-sans">
                            Hizmet ücretsiz olarak ve "olduğu gibi" sunulduğundan, yürürlükteki mevzuatın izin verdiği azami ölçüde Syncrun; dolaylı, arızi, özel, cezai veya neticesel zararlardan (gelir kaybı, veri kaybı, iş kesintisi, itibar zararı dâhil) sorumlu değildir. Pilot programı süresince Syncrun'ın toplam maddi tazminat yükümlülüğü Pilot Kullanıcı başına <strong>1.000 (bin) Türk Lirası</strong> ile sınırlıdır. Bu sınır, kasıt veya ağır kusur hâlleri için uygulanmaz.
                        </p>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">9. Veri Silme ve Pilot Sonu</h3>
                        <ul className="list-disc pl-5 mt-2 space-y-1 font-sans">
                            <li>Pilot Kullanıcı, hesap paneli üzerinden veya destek talebi ile <strong>her zaman</strong> tüm verilerinin silinmesini talep edebilir.</li>
                            <li>Pilot programının resmi olarak sona ermesi hâlinde, Pilot Kullanıcı'ya en az 30 gün önceden bildirim yapılır ve bu süre içinde Pilot Kullanıcı verilerini dışa aktarabilir.</li>
                            <li>Pilot Kullanıcı talep etmediği takdirde, pilot bitiminden 30 gün sonra toplantı kayıtları, transkriptler ve hesap verisi geri dönüşümsüz olarak silinir; anonim hâle getirilmiş analiz verisi (kişisel veri niteliğinde olmayan) saklanabilir.</li>
                        </ul>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">10. Geri Bildirim Lisansı</h3>
                        <p className="font-sans">
                            Pilot Kullanıcı tarafından sağlanan geri bildirimler, hata raporları, öneriler ve ürün iyileştirme fikirleri için Pilot Kullanıcı, Syncrun'a münhasır olmayan, geri alınamaz, süresiz, dünya çapında geçerli, telifsiz bir <strong>kullanım ve uyarlama lisansı</strong> verir. Geri bildirimler üzerinde Pilot Kullanıcı'nın patent veya fikri mülkiyet talebi doğmaz.
                        </p>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">11. Bildirim ve Tebligat</h3>
                        <p className="font-sans">
                            Taraflar arasındaki tüm bildirimler, Pilot Kullanıcı'nın hesap kaydında belirttiği e-posta adresi ile Syncrun'ın <strong>support@syncrun.net</strong> adresi üzerinden yapılır. E-posta gönderiminden 24 saat sonra bildirim tebliğ edilmiş sayılır.
                        </p>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">12. Uygulanacak Hukuk ve Uyuşmazlık</h3>
                        <p className="font-sans">
                            Bu Sözleşme Türkiye Cumhuriyeti hukukuna tabidir. Sözleşmeden doğacak uyuşmazlıklarda <strong>İstanbul Mahkemeleri ve İcra Daireleri</strong> yetkilidir. Tüketici sıfatına sahip Pilot Kullanıcılar için 6502 sayılı Tüketicinin Korunması Hakkında Kanun'da öngörülen yetkili merciler saklıdır.
                        </p>

                        <h3 className="text-lg text-text mt-6 mb-2 font-serif">13. Kabul Beyanı</h3>
                        <p className="font-sans">
                            Pilot Kullanıcı; bu Sözleşme'yi, Kullanım Koşulları'nı, Gizlilik Politikası'nı ve KVKK Aydınlatma Metni'ni okuduğunu, anladığını ve kayıt sırasında bunları kabul ettiğine dair onay kutusunu işaretlemekle bu maddelerin tamamını kabul ettiğini beyan eder. Elektronik onay, ıslak imza ile eşdeğer hukuki sonuç doğurur (6098 sayılı Türk Borçlar Kanunu m.14 ve 5070 sayılı Elektronik İmza Kanunu çerçevesinde).
                        </p>

                        <p className="mt-8 font-sans text-sm text-text-tertiary">
                            Bu metnin güncel sürümüne <a href="/pilot-sozlesmesi" className="text-accent hover:text-accent-hover">syncrun.net/pilot-sozlesmesi</a> adresinden ulaşabilirsiniz. Sözleşme'nin önceki sürümlerini görmek ya da kabul kaydınızın bir kopyasını talep etmek için support@syncrun.net adresine yazabilirsiniz.
                        </p>
                    </div>
                </div>
            </div>
        </div>
    )
}

export default PilotSozlesmesi
